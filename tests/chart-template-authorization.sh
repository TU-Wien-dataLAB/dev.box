#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"

assert_policy() {
  local scenario="$1"
  shift
  helm template template-authz "$chart" --namespace containerssh \
    --set kata.enabled=false \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.test \
    --set authServer.authentik.tokenSecret=authentik-read-token "$@" |
    ruby -r yaml -r json -e '
      docs = YAML.load_stream(STDIN.read).compact
      auth = docs.find { |o| o["kind"] == "Deployment" && o.dig("metadata", "name").end_with?("-auth-server") }
      env = auth.dig("spec", "template", "spec", "containers", 0, "env")
      policy = env.find { |e| e["name"] == "AUTH_SERVER_ALLOWED_TEMPLATES" }
      if ARGV[0] == "disabled"
        abort "template gate unexpectedly enabled without bundled config server" if policy
      else
        abort "missing template authorization policy" unless policy
        names = JSON.parse(policy.fetch("value"))
        templates = docs.find { |o| o["kind"] == "ConfigMap" && o.dig("metadata", "name").end_with?("-templates") }
        expected = (templates["data"] || {}).keys.map { |name| name.delete_suffix(".yaml") }
        abort "authorization policy does not match rendered template names" unless names.sort == expected.sort
        abort "wrong named policy" if ARGV[0] == "named" && names != ["ubuntu", "default"]
        abort "empty template list must deny all" if ARGV[0] == "empty" && names != []
        abort "implicit template name mismatch" if ARGV[0] == "implicit" && names != ["template-0"]
      end
    ' "$scenario"
  printf 'OK   template authorization %s\n' "$scenario"
}

assert_policy named --set configServer.enabled=true \
  --set 'kubernetes.podTemplates[0].name=ubuntu' \
  --set 'kubernetes.podTemplates[1].name=default'
assert_policy empty --set configServer.enabled=true
assert_policy implicit --set configServer.enabled=true \
  --set 'kubernetes.podTemplates[0].metadata.labels.template=implicit'
assert_policy disabled --set kubernetes.mode=connection
