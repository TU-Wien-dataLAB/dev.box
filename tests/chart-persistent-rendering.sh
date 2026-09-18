#!/usr/bin/env bash
# Validate the persistent-mode chart rendering surface:
#   * default kubernetes.mode renders persistent + createMissingPods: true and
#     no metadata.generateName;
#   * when a chart template explicitly selects a non-persistent mode, base-level
#     createMissingPods is omitted and injected only into persistent responses;
#   * connection mode keeps generateName and has no createMissingPods;
#   * persistent mode without a config server (no bundled server, no external
#     configserver.url) fails rendering with an actionable message;
#   * the per-user box cap reaches the config-server deployment as
#     CONTAINERSSH_MAX_PODS_PER_USER;
#   * the config-server's list-pods ServiceAccount/Role/RoleBinding render only
#     when configServer.enabled=true, and live in the session namespace.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
release="persistent-test"
namespace="containerssh"
persistent_required_message="persistent kubernetes mode needs a config server to inject stable per-user pod names: enable configServer or set configserver.url (or use kubernetes.mode=connection)"

render_config() {
  helm template "$release" "$chart" --namespace "$namespace" \
    --show-only templates/configmap.yaml "$@" |
    ruby -ryaml -e '
      document = YAML.load_stream(STDIN.read).find { |item| item.is_a?(Hash) && item["kind"] == "ConfigMap" }
      abort "rendered ContainerSSH ConfigMap not found" unless document
      print document.fetch("data").fetch("config.yaml")
    '
}

render_all() {
  helm template "$release" "$chart" --namespace "$namespace" "$@"
}

# --- default mode: persistent + createMissingPods, no generateName ----------
config="$(render_config --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true)"
printf '%s' "$config" | ruby -ryaml -e '
  config = YAML.load(STDIN.read)
  pod = config.dig("kubernetes", "pod")
  abort "mode is not persistent" unless pod.dig("mode") == "persistent"
  abort "createMissingPods not rendered in persistent mode" unless pod["createMissingPods"] == true
  metadata = pod.dig("metadata") || abort("pod metadata missing")
  abort "generateName must not be force-rendered in persistent mode" if metadata.key?("generateName")
  abort "session namespace not rendered" unless metadata.dig("namespace") == "containerssh-sessions"
'
printf 'OK   default mode = persistent, createMissingPods=true, no generateName\n'

# --- mixed mode omits base createMissingPods so connection templates work ---
config="$(render_config --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true \
  --set kubernetes.podTemplates[0].name=per-connection \
  --set kubernetes.podTemplates[0].mode=connection)"
printf '%s' "$config" | ruby -ryaml -e '
  config = YAML.load(STDIN.read)
  pod = config.dig("kubernetes", "pod")
  abort "mode is not persistent" unless pod.dig("mode") == "persistent"
  abort "base createMissingPods makes a connection-mode template invalid" if pod.key?("createMissingPods")
'
printf 'OK   mixed-mode templates use request-scoped createMissingPods\n'

# --- connection mode keeps generateName and no createMissingPods ------------
config="$(render_config --set kubernetes.mode=connection \
  --set authServer.enabled=true --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token)"
printf '%s' "$config" | ruby -ryaml -e '
  config = YAML.load(STDIN.read)
  pod = config.dig("kubernetes", "pod")
  abort "mode is not connection" unless pod.dig("mode") == "connection"
  abort "createMissingPods rendered outside persistent mode" if pod.key?("createMissingPods")
  metadata = pod.dig("metadata") || abort("pod metadata missing")
  abort "generateName missing in connection mode" unless metadata.dig("generateName") == "containerssh-"
'
printf 'OK   connection mode keeps generateName, no createMissingPods\n'

# --- config changes roll the main ContainerSSH deployment --------------------
deployment_checksum() {
  render_all "$@" |
    ruby -ryaml -e '
      docs = YAML.load_stream(STDIN.read).compact
      deployment = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name") == "persistent-test-containerssh" }
      abort "ContainerSSH Deployment not rendered" unless deployment
      print deployment.dig("spec", "template", "metadata", "annotations", "checksum/config").to_s
    '
}
connection_checksum="$(deployment_checksum --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://auth.example.test)"
persistent_checksum="$(deployment_checksum --set auth.publicKey.webhook.url=https://auth.example.test \
  --set configServer.enabled=true)"
if [[ -z "$connection_checksum" || -z "$persistent_checksum" ]]; then
  printf 'FAIL main Deployment has no config checksum annotation\n' >&2
  exit 1
fi
if [[ "$connection_checksum" == "$persistent_checksum" ]]; then
  printf 'FAIL config checksum did not change between connection and persistent modes\n' >&2
  exit 1
fi
printf 'OK   config changes roll the main ContainerSSH deployment\n'

# --- persistent without a config server fails rendering ----------------------
if output="$(render_all --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token 2>&1)"; then
  printf 'FAIL persistent mode without a config server rendered unexpectedly\n' >&2
  exit 1
fi
if ! grep -Fq "$persistent_required_message" <<<"$output"; then
  printf 'FAIL expected actionable persistent-mode error, got:\n%s\n' "$output" >&2
  exit 1
fi
printf 'OK   persistent mode without a config server fails rendering\n'

# --- cap env reaches the config-server deployment ----------------------------
assert_cap() {
  local name="$1"
  local expected="$2"
  shift 2
  render_all "$@" |
    ruby -ryaml -e '
      docs = YAML.load_stream(STDIN.read).compact
      deployment = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name").end_with?("-config-server") }
      abort "config-server Deployment not rendered" unless deployment
      env = deployment.dig("spec", "template", "spec", "containers", 0, "env")
      mode = env.find { |item| item["name"] == "CONTAINERSSH_OPERATING_MODE" }
      namespace = env.find { |item| item["name"] == "CONTAINERSSH_SESSION_NAMESPACE" }
      cap = env.find { |item| item["name"] == "CONTAINERSSH_MAX_PODS_PER_USER" }
      abort "CONTAINERSSH_OPERATING_MODE not rendered" unless mode&.dig("value") == "persistent"
      abort "CONTAINERSSH_SESSION_NAMESPACE not rendered" unless namespace&.dig("value") == "containerssh-sessions"
      abort "cap default is not 3: #{cap.inspect}" unless cap&.dig("value") == ARGV.fetch(0)
    ' "$expected"
  printf 'OK   %s\n' "$name"
}

assert_cap "cap default 3 reaches the config-server deployment" 3 \
  --set authServer.enabled=true --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true
assert_cap "cap is configurable via chart value" 5 \
  --set authServer.enabled=true --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true \
  --set configServer.maxPodsPerUser=5

# --- list-pods RBAC renders only when the config server is enabled -----------
assert_rbac() {
  local name="$1"
  local enabled="$2"
  shift 2
  render_all "$@" |
    ruby -ryaml -e '
      docs = YAML.load_stream(STDIN.read).compact
      suffix = "-config-server"
      sa = docs.any? { |d| d["kind"] == "ServiceAccount" && d.dig("metadata", "name").end_with?(suffix) }
      role = docs.find { |d| d["kind"] == "Role" && d.dig("metadata", "name").end_with?(suffix) }
      binding = docs.any? { |d| d["kind"] == "RoleBinding" && d.dig("metadata", "name").end_with?(suffix) }
      expected = (ARGV.fetch(0) == "true")
      abort "config-server ServiceAccount = #{sa}, want #{expected}" unless sa == expected
      abort "config-server RoleBinding = #{binding}, want #{expected}" unless binding == expected
      if expected
        abort "config-server Role missing" unless role
        rules = role["rules"] || abort("Role has no rules")
        abort "Role must limit to list pods: #{rules.inspect}" unless rules == [{ "apiGroups" => [""], "resources" => ["pods"], "verbs" => ["list"] }]
        abort "Role must live in the session namespace" unless role.dig("metadata", "namespace") == "containerssh-sessions"
      end
    ' "$enabled"
  printf 'OK   %s\n' "$name"
}

assert_rbac "config-server list-pods RBAC rendered when enabled" true \
  --set authServer.enabled=true --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true
assert_rbac "no config-server RBAC when the server is disabled" false \
  --set kubernetes.mode=connection \
  --set authServer.enabled=true --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token
