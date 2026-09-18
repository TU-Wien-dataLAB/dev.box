#!/usr/bin/env bash
# Feed rendered and representative webhook-merged configs to the real
# ContainerSSH v0.6 binary. This catches strict YAML, upstream config-schema,
# and persistent/non-persistent mode compatibility failures that Helm parsing
# alone cannot detect.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
image="${CONTAINERSSH_IMAGE:-containerssh/containerssh:v0.6}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf 'fake-token\n' >"$tmp/token"
printf 'fake-ca\n' >"$tmp/ca.crt"

render_config() {
  local mode="$1"
  shift
  helm template "binary-${mode}" "$chart" --namespace containerssh \
    --set "kubernetes.mode=${mode}" \
    --set auth.publicKey.webhook.url=https://auth.example.test \
    "$@" |
    ruby -ryaml -e '
      document = YAML.load_stream(STDIN.read).find do |item|
        item.is_a?(Hash) && item["kind"] == "ConfigMap" && item.dig("data", "config.yaml")
      end
      abort "rendered ContainerSSH ConfigMap not found" unless document
      print document.fetch("data").fetch("config.yaml")
    '
}

# Apply a representative config-webhook pod override to a rendered base. The
# recursive map merge mirrors the shape relevant here: mode, createMissingPods,
# metadata.name and labels.
merge_pod_override() {
  local base="$1"
  local override="$2"
  local output="$3"
  ruby -ryaml -e '
    def deep_merge!(destination, source)
      source.each do |key, value|
        if destination[key].is_a?(Hash) && value.is_a?(Hash)
          deep_merge!(destination[key], value)
        else
          destination[key] = value
        end
      end
    end
    base = YAML.load_file(ARGV.fetch(0))
    override = YAML.load_file(ARGV.fetch(1))
    deep_merge!(base, override)
    File.write(ARGV.fetch(2), YAML.dump(base))
  ' "$base" "$override" "$output"
}

validate_config() {
  local label="$1"
  local config="$2"
  local stderr="$tmp/stderr-${label}.log"

  docker run --rm \
    -v "$config:/etc/containerssh/config.yaml:ro" \
    -v "$tmp/token:/var/run/secrets/kubernetes.io/serviceaccount/token:ro" \
    -v "$tmp/ca.crt:/var/run/secrets/kubernetes.io/serviceaccount/ca.crt:ro" \
    "$image" --config /etc/containerssh/config.yaml --dump-config \
    >"$tmp/dump-${label}.json" 2>"$stderr"

  if grep -q CORE_CONFIG_ERROR "$stderr"; then
    printf 'FAIL %s config produced CORE_CONFIG_ERROR:\n' "$label" >&2
    cat "$stderr" >&2
    exit 1
  fi
  printf 'OK   real ContainerSSH binary accepts %s config\n' "$label"
}

render_config connection >"$tmp/config-connection.yaml"
render_config persistent --set configServer.enabled=true >"$tmp/config-persistent-base.yaml"
render_config persistent \
  --set configServer.enabled=true \
  --set kubernetes.podTemplates[0].name=per-connection \
  --set kubernetes.podTemplates[0].mode=connection \
  >"$tmp/config-persistent-mixed-base.yaml"
validate_config connection "$tmp/config-connection.yaml"
validate_config persistent-base "$tmp/config-persistent-base.yaml"

cat >"$tmp/override-persistent.yaml" <<'YAML'
kubernetes:
  pod:
    createMissingPods: true
    metadata:
      name: box-0123456789
      labels:
        dev.box/owner: alice
YAML
merge_pod_override "$tmp/config-persistent-base.yaml" "$tmp/override-persistent.yaml" \
  "$tmp/config-persistent-effective.yaml"
validate_config persistent-effective "$tmp/config-persistent-effective.yaml"

cat >"$tmp/override-connection-template.yaml" <<'YAML'
kubernetes:
  pod:
    mode: connection
    metadata:
      labels:
        dev.box/template: per-connection
YAML
merge_pod_override "$tmp/config-persistent-mixed-base.yaml" "$tmp/override-connection-template.yaml" \
  "$tmp/config-connection-template-effective.yaml"
validate_config connection-template-effective "$tmp/config-connection-template-effective.yaml"
