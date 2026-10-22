#!/usr/bin/env bash
# Unconditional shared-base restrictions and ordinary-template escape hatch.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

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
