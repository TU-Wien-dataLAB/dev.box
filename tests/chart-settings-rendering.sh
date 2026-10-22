#!/usr/bin/env bash
# Code-owned settings wiring + ordinary persistent positive control.
# The synthesized execution/security policy is covered at the webhook/merge seams.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# The application is code-owned even when the bundled config server is off.
for bundled in true false; do
  if helm template settings "$root/charts/containerssh" \
    --set configServer.enabled="$bundled" --set kubernetes.mode=connection \
    --set auth.publicKey.webhook.url=https://auth.example.test \
    --set 'kubernetes.podTemplates[0].name=settings' >"$tmp/reserved.log" 2>&1; then
    printf 'FAIL reserved settings template accepted (bundled=%s)\n' "$bundled" >&2
    exit 1
  fi
  grep -q 'settings is reserved' "$tmp/reserved.log"
done
printf 'OK   settings is reserved regardless of config-server enablement\n'

ruby -ryaml -e '
  File.write(ARGV[0], YAML.dump({"kubernetes" => {"podTemplates" => [{"name" => "ubuntu", "metadata" => {"labels" => {"template" => "ubuntu"}}}]}}))
' "$tmp/values.yaml"
helm template settings "$root/charts/containerssh" --namespace containerssh \
  --set configServer.enabled=true --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=existing-token -f "$tmp/values.yaml" >"$tmp/rendered.yaml"
ruby -ryaml -rjson -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  templates = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("metadata", "name").end_with?("-templates") }.fetch("data")
  abort "settings must not be rendered as a template file" unless templates.keys == ["ubuntu.yaml"]
  server = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name").end_with?("-config-server") }
  env = server.dig("spec", "template", "spec", "containers", 0, "env").to_h { |e| [e.fetch("name"), e["value"]] }
  settings_image = YAML.load_file(ARGV[1]).fetch("settings").fetch("image")
  abort "settings default must be digest-pinned" unless settings_image.fetch("tag").match?(/(?:@|\A)sha256:[0-9a-f]{64}\z/)
  separator = settings_image.fetch("tag").start_with?("sha256:") ? "@" : ":"
  expected_image = settings_image.fetch("repository") + separator + settings_image.fetch("tag")
  abort "wrong settings image default" unless env["CONTAINERSSH_SETTINGS_IMAGE"] == expected_image
  abort "template directory not authoritative" unless env["CONTAINERSSH_CONFIG_DIR"] == "/config" && !env.keys.any? { |key| key.include?("TEMPLATE") }
  abort "template directory is not mounted" unless server.dig("spec", "template", "spec", "volumes").any? { |v| v.dig("configMap", "name") == "settings-containerssh-templates" }
  ubuntu = YAML.load(templates.fetch("ubuntu.yaml"))
  abort "ordinary template changed mode/security" if ubuntu["security"] || ubuntu.dig("kubernetes", "pod", "mode")
  base = YAML.load(docs.find { |d| d["kind"] == "ConfigMap" && d.dig("data", "config.yaml") }.dig("data", "config.yaml"))
  abort "base lost persistence" unless base.dig("kubernetes", "pod", "mode") == "persistent"
  spec = base.dig("kubernetes", "pod", "spec")
  abort "ordinary guest must be nonroot" unless spec.dig("securityContext", "runAsNonRoot") && spec.dig("securityContext", "runAsUser") == 1000
  container = spec.fetch("containers").fetch(0)
  abort "ordinary guest privilege escalation" unless container.dig("securityContext", "allowPrivilegeEscalation") == false
  abort "ordinary guest limits missing" unless container.dig("resources", "limits", "memory") && container.dig("resources", "limits", "cpu")
  abort "ordinary execution restricted globally" if base["security"]
  abort "base runtime leaks into settings" if spec.key?("runtimeClassName")
  abort "ordinary template lost Kata" unless ubuntu.dig("kubernetes", "pod", "spec", "runtimeClassName") == "kata-qemu-runtime-rs"
  abort "persistent-only creation leaks" if base.dig("kubernetes", "pod").key?("createMissingPods")
  gateway = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name") == "settings-containerssh" }
  abort "default gateway must support settings and auth denial messages" unless gateway.dig("spec", "template", "spec", "containers", 0, "image") == "ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d"
  auth = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name").end_with?("-auth-server") }
  policy = auth.dig("spec", "template", "spec", "containers", 0, "env").find { |e| e["name"] == "AUTH_SERVER_ALLOWED_TEMPLATES" }
  abort "settings bypasses exact-name authz gate" unless JSON.parse(policy.fetch("value")) == ["ubuntu", "settings"]
' "$tmp/rendered.yaml" "$root/charts/containerssh/values.yaml"
printf 'OK   code-owned settings image/directory wiring and ordinary security defaults\n'

# Tags and digest pins use the same two-value interface. Startup requires an
# image even in connection mode; mounted files remain the template authority.
for tag in 'sha-custom' \
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  'sha-custom@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'; do
  helm template settings "$root/charts/containerssh" \
    --set configServer.enabled=true --set kata.enabled=false \
    --set kubernetes.mode=connection --set kubernetes.sessionNamespace=custom-sessions \
    --set auth.publicKey.webhook.url=https://auth.example.test \
    --set settings.image.repository=registry.example.test/settings \
    --set-string "settings.image.tag=$tag" >"$tmp/image.yaml"
  ruby -ryaml -e '
    docs = YAML.load_stream(File.read(ARGV[0])).compact
    server = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name").end_with?("-config-server") }
    spec = server.dig("spec", "template", "spec")
    env = spec.dig("containers", 0, "env").to_h { |e| [e.fetch("name"), e["value"]] }
    separator = ARGV[1].start_with?("sha256:") ? "@" : ":"
    abort "settings image override lost" unless env["CONTAINERSSH_SETTINGS_IMAGE"] == "registry.example.test/settings#{separator}#{ARGV[1]}"
    abort "connection-mode namespace missing" unless env["CONTAINERSSH_SESSION_NAMESPACE"] == "custom-sessions"
    abort "wrong operating mode" unless env["CONTAINERSSH_OPERATING_MODE"] == "connection"
    abort "in-cluster client lacks service account" unless spec["serviceAccountName"] == "settings-containerssh-config-server"
    role = docs.find { |d| d["kind"] == "Role" && d.dig("metadata", "name").end_with?("-config-server") }
    binding = docs.find { |d| d["kind"] == "RoleBinding" && d.dig("metadata", "name").end_with?("-config-server") }
    abort "connection-mode RBAC missing or in wrong namespace" unless role.dig("metadata", "namespace") == "custom-sessions" && binding.dig("metadata", "namespace") == "custom-sessions"
    templates = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("metadata", "name").end_with?("-templates") }
    abort "settings must not require a file" unless (templates["data"] || {}).empty?
  ' "$tmp/image.yaml" "$tag"
done
for key in repository tag; do
  for empty in '' null; do
    if helm template settings "$root/charts/containerssh" \
      --set configServer.enabled=true --set kubernetes.mode=connection \
      --set auth.publicKey.webhook.url=https://auth.example.test \
      --set "settings.image.$key=$empty" >"$tmp/bad.log" 2>&1; then
      printf 'FAIL missing settings image %s accepted\n' "$key" >&2
      exit 1
    fi
    grep -q "settings.image.$key is required" "$tmp/bad.log"
  done
done
printf 'OK   settings tag/digest overrides, required image, and connection-mode client/RBAC wiring\n'
