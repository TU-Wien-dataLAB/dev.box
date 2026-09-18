#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
release="auth-test"
namespace="containerssh"
required_auth_message="configure at least one authentication method: enable authServer or set auth.password.webhook.url or auth.publicKey.webhook.url"

expect_render_failure() {
  local name="$1"
  shift
  local output
  if output="$(helm template "$release" "$chart" --namespace "$namespace" "$@" 2>&1)"; then
    printf 'FAIL %s: render unexpectedly succeeded\n' "$name" >&2
    exit 1
  fi
  if ! grep -Fq "$required_auth_message" <<<"$output"; then
    printf 'FAIL %s: expected actionable auth error, got:\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'OK   %s\n' "$name"
}

render_config() {
  helm template "$release" "$chart" --namespace "$namespace" \
    --show-only templates/configmap.yaml "$@" |
    ruby -ryaml -e '
      document = YAML.load_stream(STDIN.read).find { |item| item.is_a?(Hash) && item["kind"] == "ConfigMap" }
      abort "rendered ContainerSSH ConfigMap not found" unless document
      print document.fetch("data").fetch("config.yaml")
    '
}

assert_config() {
  local name="$1"
  local scenario="$2"
  shift 2
  local config
  config="$(render_config "$@")"
  printf '%s' "$config" | ruby -ryaml -e '
    config = YAML.load(STDIN.read)
    auth = config.fetch("auth")
    scenario = ARGV.fetch(0)
    bundled = "http://auth-test-containerssh-auth-server.containerssh.svc.cluster.local:8080"
    bundled_config = "http://auth-test-containerssh-config-server.containerssh.svc.cluster.local:8080"

    case scenario
    when "password"
      abort "wrong auth methods: #{auth.keys.inspect}" unless auth.keys.sort == ["password"]
      abort "wrong password URL" unless auth.dig("password", "webhook", "url") == "https://password.example.test"
    when "publicKey"
      abort "wrong auth methods: #{auth.keys.inspect}" unless auth.keys.sort == ["publicKey"]
      abort "wrong pubkey URL" unless auth.dig("publicKey", "webhook", "url") == "https://pubkey.example.test"
    when "publicKey-authz"
      abort "wrong auth methods: #{auth.keys.inspect}" unless auth.keys.sort == ["authz", "publicKey"]
      abort "wrong pubkey URL" unless auth.dig("publicKey", "webhook", "url") == "https://pubkey.example.test"
      abort "wrong authz URL" unless auth.dig("authz", "webhook", "url") == "https://authz.example.test"
    when "bundled"
      abort "wrong auth methods: #{auth.keys.inspect}" unless auth.keys.sort == ["authz", "publicKey"]
      auth.each_value do |method|
        abort "wrong bundled URL" unless method.dig("webhook", "url") == bundled
      end
      abort "wrong bundled config URL" unless config.dig("configserver", "url") == bundled_config
    when "mixed"
      abort "wrong password override" unless auth.dig("password", "webhook", "url") == "https://password.example.test"
      abort "wrong bundled pubkey URL" unless auth.dig("publicKey", "webhook", "url") == bundled
      abort "wrong bundled authz URL" unless auth.dig("authz", "webhook", "url") == bundled
    else
      abort "unknown scenario #{scenario}"
    end

    auth.each do |name, method|
      abort "#{name} method is not webhook" unless method["method"] == "webhook"
      abort "#{name} has an empty URL" if method.dig("webhook", "url").to_s.empty?
      abort "#{name} has the wrong request timeout" unless method.dig("webhook", "timeout") == "30s"
    end
  ' "$scenario"
  printf 'OK   %s\n' "$name"
}

expect_render_failure "no authentication configured"
expect_render_failure "authorization without authentication" \
  --set auth.authz.webhook.url=https://authz.example.test

assert_config "external password only" password \
  --set kubernetes.mode=connection \
  --set auth.password.webhook.url=https://password.example.test
assert_config "external public key only" publicKey \
  --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://pubkey.example.test
assert_config "external public key plus authorization" publicKey-authz \
  --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://pubkey.example.test \
  --set auth.authz.webhook.url=https://authz.example.test
# Bundled auth + config servers stay on the default PERSISTENT mode to prove
# the chart renders persistent + a config server (the dev.box target).
assert_config "bundled auth and config servers" bundled \
  --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true
assert_config "explicit method overrides bundled URL" mixed \
  --set kubernetes.mode=connection \
  --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set auth.password.webhook.url=https://password.example.test

helm template "$release" "$chart" --namespace "$namespace" \
  --set existingConfigMap=external-containerssh-config >/dev/null
printf 'OK   externally managed config bypasses chart auth validation\n'

helm lint "$chart" --set kubernetes.mode=connection --set auth.publicKey.webhook.url=https://pubkey.example.test >/dev/null
printf 'OK   chart lint with valid authentication\n'
