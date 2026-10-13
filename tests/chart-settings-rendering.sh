#!/usr/bin/env bash
# Exact-name application template + ordinary persistent positive control.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
ruby -ryaml -e '
  settings = YAML.load_file(ARGV[0])
  File.write(ARGV[1], YAML.dump({"kubernetes" => {"podTemplates" => [{"name" => "ubuntu", "metadata" => {"labels" => {"template" => "ubuntu"}}}, settings]}}))
' "$root/charts/containerssh/examples/settings-template.yaml" "$tmp/values.yaml"
helm template settings "$root/charts/containerssh" --namespace containerssh \
  --set configServer.enabled=true --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=existing-token -f "$tmp/values.yaml" >"$tmp/rendered.yaml"
ruby -ryaml -rjson -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  templates = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("metadata", "name").end_with?("-templates") }.fetch("data")
  abort "exact template list changed" unless templates.keys.sort == ["settings.yaml", "ubuntu.yaml"]
  settings = YAML.load(templates.fetch("settings.yaml"))
  pod = settings.dig("kubernetes", "pod")
  abort "settings is not disposable" unless pod["mode"] == "connection" && !pod["createMissingPods"] && !pod.dig("metadata", "name")
  abort "wrong default application" unless pod["shellCommand"] == ["/usr/local/bin/dev-box-settings"]
  security = settings.fetch("security")
  abort "not default deny" unless security["defaultMode"] == "disable" && security["maxSessions"] == 1
  abort "wrong forced app" unless security["forceCommand"] == "/usr/local/bin/dev-box-settings"
  %w[shell tty].each { |kind| abort "#{kind} disabled" unless security.dig(kind, "mode") == "enable" }
  %w[command subsystem env signal].each { |kind| abort "#{kind} allowed" unless security.dig(kind, "mode") == "disable" }
  abort "forwarding allowed" unless security.fetch("forwarding").values.all? { |v| v == "disable" }
  spec = pod.fetch("spec")
  abort "guest token mounted" unless spec["automountServiceAccountToken"] == false
  abort "guest host/persistent mounts" if spec["volumes"] || spec["hostNetwork"] || spec["hostPID"] || spec["hostIPC"] || spec["serviceAccountName"]
  abort "guest must be nonroot" unless spec.dig("securityContext", "runAsNonRoot") && spec.dig("securityContext", "runAsUser") == 1000
  container = spec.fetch("containers").fetch(0)
  abort "guest privilege escalation" unless container.dig("securityContext", "allowPrivilegeEscalation") == false
  abort "guest limits missing" unless container.dig("resources", "limits", "memory") && container.dig("resources", "limits", "cpu")
  abort "owner was baked into template" if container["env"]
  abort "settings consumes box quota" if pod.dig("metadata", "labels", "dev.box/owner")
  ubuntu = YAML.load(templates.fetch("ubuntu.yaml"))
  abort "ordinary template changed mode/security" if ubuntu["security"] || ubuntu.dig("kubernetes", "pod", "mode")
  base = YAML.load(docs.find { |d| d["kind"] == "ConfigMap" && d.dig("data", "config.yaml") }.dig("data", "config.yaml"))
  abort "base lost persistence" unless base.dig("kubernetes", "pod", "mode") == "persistent"
  abort "persistent-only creation leaks" if base.dig("kubernetes", "pod", "createMissingPods")
  abort "ordinary execution restricted globally" if base["security"]
  abort "runtime isolation lost" unless base.dig("kubernetes", "pod", "spec", "runtimeClassName") == "kata-qemu-runtime-rs"
  auth = docs.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name").end_with?("-auth-server") }
  policy = auth.dig("spec", "template", "spec", "containers", 0, "env").find { |e| e["name"] == "AUTH_SERVER_ALLOWED_TEMPLATES" }
  abort "settings bypasses exact-name authz gate" unless JSON.parse(policy.fetch("value")) == ["ubuntu", "settings"]
' "$tmp/rendered.yaml"
printf 'OK   settings execution/security, least privilege, template authz, and persistent defaults\n'

# Nil/false partial overrides cannot clear dangerous inherited base pod fields.
for field in 'spec.volumes[0].name=home' 'spec.serviceAccountName=management' \
  'spec.hostNetwork=true' 'spec.automountServiceAccountToken=true' \
  'spec.initContainers[0].name=setup' 'metadata.name=shared' \
  'metadata.labels.dev\.box/owner=alice'; do
  if helm template settings "$root/charts/containerssh" --namespace containerssh \
    --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
    -f "$tmp/values.yaml" --set "kubernetes.pod.$field" >"$tmp/bad.log" 2>&1; then
    printf 'FAIL settings inherited unsafe base %s\n' "$field" >&2
    exit 1
  fi
  grep -q 'settings cannot inherit' "$tmp/bad.log"
done
printf 'OK   settings refuses unsafe inherited base pod fields\n'
