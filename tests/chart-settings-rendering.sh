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
  abort "wrong settings image default" unless env["CONTAINERSSH_SETTINGS_IMAGE"] == "ghcr.io/tu-wien-datalab/dev.box/settings-tui:sha-901d8a7eb8092db87b4fbd44fbed64beb74c1de3@sha256:08de4f9e7d47a5928b147591e4349bbb0183483920bbcd29fd13d2730841c1f2"
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
' "$tmp/rendered.yaml"
printf 'OK   code-owned settings image/directory wiring and ordinary security defaults\n'

# Nil/false/empty partial overrides cannot clear inherited base fields. Reject
# keys by presence, even without settings files or bundled webhook/config maps.
for field in 'spec.volumes[0].name=home' 'spec.serviceAccountName=management' \
  'spec.serviceAccount=legacy' 'spec.hostNetwork=true' 'spec.hostPID=false' \
  'spec.hostIPC=true' 'spec.shareProcessNamespace=true' \
  'spec.automountServiceAccountToken=true' 'spec.automountServiceAccountToken=false' \
  'spec.initContainers[0].name=setup' 'spec.ephemeralContainers[0].name=debug' \
  'spec.runtimeClassName=external' 'spec.runtimeClassName=' \
  'spec.nodeName=worker' 'spec.nodeSelector.pool=privileged' \
  'spec.affinity.nodeAffinity={}' 'spec.tolerations[0].operator=Exists' \
  'spec.imagePullSecrets[0].name=private' 'spec.hostAliases[0].ip=127.0.0.1' \
  'spec.dnsPolicy=Default' 'spec.dnsConfig.nameservers[0]=1.1.1.1' \
  'spec.hostname=shared' 'spec.subdomain=internal' 'spec.setHostnameAsFQDN=true' \
  'spec.hostUsers=false' 'spec.os.name=windows' \
  'spec.schedulerName=custom' 'spec.priorityClassName=system-node-critical' \
  'spec.priority=1000' 'spec.preemptionPolicy=PreemptLowerPriority' \
  'spec.activeDeadlineSeconds=1' 'spec.readinessGates[0].conditionType=test' \
  'spec.schedulingGates[0].name=test' 'spec.topologySpreadConstraints[0].maxSkew=1' \
  'spec.enableServiceLinks=true' 'spec.overhead.cpu=1' \
  'spec.resourceClaims[0].name=claim' 'spec.resources.limits.cpu=1' \
  'spec.futureUnsafeField=true' \
  'metadata.name=shared' 'metadata.name=' 'metadata.generateName=shared-' \
  'metadata.namespace=other' 'metadata.ownerReferences[0].name=foreign' \
  'metadata.finalizers[0]=keep' 'metadata.labels.dev\.box/owner=alice'; do
  for bundled in true false; do
    extra=()
    if [[ "$bundled" == false ]]; then extra+=(--set existingConfigMap=external); fi
    if helm template settings "$root/charts/containerssh" --namespace containerssh \
      --set configServer.enabled="$bundled" --set kubernetes.mode=connection \
      --set auth.publicKey.webhook.url=https://auth.example.test \
      "${extra[@]}" --set "kubernetes.pod.$field" >"$tmp/bad.log" 2>&1; then
      printf 'FAIL unsafe base %s accepted (bundled=%s)\n' "$field" "$bundled" >&2
      exit 1
    fi
    grep -q 'unsafe shared base kubernetes.pod.' "$tmp/bad.log"
  done
done
printf 'OK   unconditional shared-base allowlist rejects unsafe fields, including false/empty values\n'

# PodSecurityContext is merged recursively, not replaced like containers.
# Settings only overwrites these four fields; seccompProfile is itself nested.
for field in 'fsGroup=0' 'fsGroupChangePolicy=Always' \
  'supplementalGroups[0]=0' 'supplementalGroupsPolicy=Merge' \
  'sysctls[0].name=kernel.shm_rmid_forced' 'seLinuxOptions.type=unconfined_t' \
  'seLinuxChangePolicy=Recursive' 'windowsOptions.hostProcess=true' \
  'appArmorProfile.type=Unconfined' 'futureUnsafeField=true' \
  'seccompProfile.localhostProfile=custom.json' 'seccompProfile.futureField=true'; do
  if helm template settings "$root/charts/containerssh" \
    --set kubernetes.mode=connection --set auth.publicKey.webhook.url=https://auth.example.test \
    --set "kubernetes.pod.spec.securityContext.$field" >"$tmp/bad.log" 2>&1; then
    printf 'FAIL unsafe nested securityContext.%s accepted\n' "$field" >&2
    exit 1
  fi
  grep -q 'unsafe shared base kubernetes.pod.spec.securityContext.' "$tmp/bad.log"
done
printf 'OK   nested shared securityContext cannot leak fields into settings\n'

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

# Empty bases are supported; a safe shared base is not mandatory.
for field in spec spec.securityContext; do
  helm template settings "$root/charts/containerssh" \
    --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
    --set "kubernetes.pod.$field=null" >"$tmp/empty.yaml"
done
printf 'OK   empty shared spec and securityContext remain supported\n'

# All heavy fields remain available per ordinary template, including security
# and execution overrides; accepted shared fields still reach ordinary boxes.
ruby -ryaml -e '
  File.write(ARGV[0], YAML.dump({"kubernetes" => {
    "pod" => {"metadata" => {"labels" => {"team" => "ssh"}, "annotations" => {"note" => "shared"}}, "spec" => {
      "restartPolicy" => "OnFailure", "terminationGracePeriodSeconds" => 45,
      "securityContext" => {"runAsNonRoot" => false, "runAsUser" => 2000, "runAsGroup" => 2000, "seccompProfile" => {"type" => "Unconfined"}}
    }},
    "podTemplates" => [{"name" => "ordinary-app", "mode" => "session", "shellCommand" => ["/opt/app"],
      "security" => {"defaultMode" => "disable", "maxSessions" => 1, "forceCommand" => "/opt/app", "shell" => {"mode" => "enable"}},
      "metadata" => {"labels" => {"template" => "ordinary-app"}},
      "spec" => {"serviceAccountName" => "ordinary", "automountServiceAccountToken" => false,
        "runtimeClassName" => "external", "hostNetwork" => true,
        "volumes" => [{"name" => "home", "emptyDir" => {}}],
        "initContainers" => [{"name" => "init", "image" => "example.test/init:v1"}],
        "securityContext" => {"fsGroup" => 2000, "supplementalGroups" => [3000]},
        "containers" => [{"name" => "app", "image" => "example.test/app:v1", "volumeMounts" => [{"name" => "home", "mountPath" => "/home"}]}]
      }}]
  }}))
' "$tmp/safe-values.yaml"
helm template settings "$root/charts/containerssh" \
  --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
  -f "$tmp/safe-values.yaml" >"$tmp/safe.yaml"
ruby -ryaml -e '
  values = YAML.load_file(ARGV[0]).fetch("kubernetes")
  docs = YAML.load_stream(File.read(ARGV[1])).compact
  config = YAML.load(docs.find { |d| d.dig("data", "config.yaml") }.dig("data", "config.yaml"))
  pod = config.dig("kubernetes", "pod")
  values.dig("pod", "spec").each { |key, value| abort "safe base #{key} lost" unless pod.dig("spec", key) == value }
  abort "safe metadata lost" unless pod.dig("metadata", "labels", "team") == "ssh" && pod.dig("metadata", "annotations", "note") == "shared"
  templates = docs.find { |d| d.dig("metadata", "name").end_with?("-templates") }.fetch("data")
  app = YAML.load(templates.fetch("ordinary-app.yaml"))
  expected = values.fetch("podTemplates").first
  abort "ordinary policy override changed" unless app["security"] == expected["security"]
  expected.reject { |key, _| ["name", "security"].include?(key) }.each do |key, value|
    abort "ordinary template #{key} changed" unless app.dig("kubernetes", "pod", key) == value
  end
' "$tmp/safe-values.yaml" "$tmp/safe.yaml"
printf 'OK   safe shared fields and heavy ordinary-template overrides retain their behavior\n'

# Settings management egress: one scoped NetworkPolicy exception for settings
# pods to the Kubernetes API only; ordinary pods keep internet-only egress.
helm template settings "$root/charts/containerssh" --namespace containerssh \
  --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
  >"$tmp/policy.yaml"
ruby -ryaml -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  policies = docs.select { |d| d["kind"] == "NetworkPolicy" }
  base = policies.find { |d| d.dig("metadata", "name") == "containerssh-sessions" }
  abort "base session NetworkPolicy missing" unless base
  abort "base policy must still apply to every pod" unless base.dig("spec", "podSelector") == {}
  abort "base policy lost deny-all ingress" unless base.dig("spec", "ingress") == [] && base.dig("spec", "policyTypes") == ["Ingress", "Egress"]
  settings = policies.find { |d| d.dig("metadata", "name") == "containerssh-sessions-settings-api" }
  abort "settings API NetworkPolicy missing" unless settings
  abort "settings exception must select only settings pods" unless settings.dig("spec", "podSelector", "matchLabels") == {"dev.box/application" => "settings"}
  abort "settings exception must be egress-only" unless settings.dig("spec", "policyTypes") == ["Egress"] || settings.dig("spec", "policyTypes") == ["Egress", "Ingress"] && false
  abort "settings exception must be egress-only" unless settings.dig("spec", "policyTypes") == ["Egress"]
  rules = settings.dig("spec", "egress")
  abort "settings exception must be exactly one rule" unless rules.size == 1
  peers = rules.first.fetch("to")
  ports = rules.first.fetch("ports")
  abort "default peer must target kube-system kube-apiserver pods" unless peers == [{
    "namespaceSelector" => {"matchLabels" => {"kubernetes.io/metadata.name" => "kube-system"}},
    "podSelector" => {"matchLabels" => {"component" => "kube-apiserver"}}}]
  abort "default ports must be TCP 443 and 6443 only" unless ports.sort_by { |p| p["port"] } == [
    {"port" => 443, "protocol" => "TCP"}, {"port" => 6443, "protocol" => "TCP"}]
' "$tmp/policy.yaml"
printf 'OK   settings-scoped Kubernetes API egress exception with unchanged ordinary policy\n'

for flag in 'networkPolicy.settingsApiEgress.enabled=false' 'networkPolicy.enabled=false'; do
  helm template settings "$root/charts/containerssh" \
    --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
    --set "$flag" >"$tmp/policy-off.yaml"
  if grep -q 'containerssh-sessions-settings-api' "$tmp/policy-off.yaml"; then
    printf 'FAIL settings API egress rendered with %s\n' "$flag" >&2
    exit 1
  fi
done
printf 'OK   settings API egress exception disabled by its own and the global switch\n'

# Operators can retarget the exception (e.g. a k3d server node) without
# touching the chart; ordinary pods remain excluded either way.
helm template settings "$root/charts/containerssh" \
  --set configServer.enabled=true --set auth.publicKey.webhook.url=https://auth.example.test \
  --set 'networkPolicy.settingsApiEgress.to[0].ipBlock.cidr=192.168.100.10/32' \
  --set 'networkPolicy.settingsApiEgress.ports[0].port=6443' \
  --set 'networkPolicy.settingsApiEgress.ports[0].protocol=TCP' >"$tmp/policy-custom.yaml"
ruby -ryaml -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  settings = docs.find { |d| d["kind"] == "NetworkPolicy" && d.dig("metadata", "name") == "containerssh-sessions-settings-api" }
  rule = settings.dig("spec", "egress").first
  abort "custom peer not rendered" unless rule.fetch("to") == [{"ipBlock" => {"cidr" => "192.168.100.10/32"}}]
  abort "custom ports not rendered" unless rule.fetch("ports") == [{"port" => 6443, "protocol" => "TCP"}]
' "$tmp/policy-custom.yaml"
printf 'OK   settings API egress peers and ports stay operator-configurable\n'
