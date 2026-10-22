#!/usr/bin/env bash
# Public seam: Helm manifests and the mounted template YAML. Requires Helm + Ruby/YAML.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
ruby -ryaml -e '
  File.write(ARGV[0], {"kubernetes" => {"podTemplates" => [{
    "name" => "ubuntu", "security" => {"maxSessions" => 1},
    "volumeClaimTemplates" => [{"metadata" => {"name" => "home"}, "spec" => {
      "accessModes" => ["ReadWriteOnce"], "storageClassName" => "cinder",
      "resources" => {"requests" => {"storage" => "25Gi"}}}}],
    "spec" => {"securityContext" => {"fsGroup" => 1000},
      "volumes" => [{"name" => "home", "persistentVolumeClaim" => {"claimName" => "home"}}],
      "containers" => [{"name" => "shell", "volumeMounts" => [{"name" => "home", "mountPath" => "/home"}],
        "env" => [{"name" => "HOME", "value" => "/home"}]}]}
  }]}}.to_yaml)
' "$tmp/managed-values.yaml"
render() {
  helm template storage-test "$chart" --namespace containerssh \
    --set kata.enabled=false --set configServer.enabled=true \
    --set auth.publicKey.webhook.url=https://auth.example.test "$@"
}
render -f "$tmp/managed-values.yaml" >"$tmp/managed.yaml"
ruby -ryaml -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  cm = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("data", "ubuntu.yaml") }
  tpl = YAML.load(cm.fetch("data").fetch("ubuntu.yaml"))
  expected = YAML.load_file(ARGV[1]).dig("kubernetes", "podTemplates", 0)
  abort "claim templates must be top-level, native PVC declarations" unless tpl["volumeClaimTemplates"] == expected["volumeClaimTemplates"]
  abort "security lost" unless tpl["security"] == expected["security"]
  abort "pod spec must pass through unchanged (no implicit storage fields)" unless tpl.dig("kubernetes", "pod", "spec") == expected["spec"]
  abort "claim templates leaked into pod" if tpl.fetch("kubernetes").fetch("pod").key?("volumeClaimTemplates")
  puts "OK template envelope preserves native claims and explicit pod fields"
' "$tmp/managed.yaml" "$tmp/managed-values.yaml"

reject() {
  local name="$1" expected="$2"
  shift 2
  if render -f "$tmp/managed-values.yaml" "$@" >"$tmp/$name.out" 2>"$tmp/$name.err"; then
    echo "FAIL $name: invalid managed storage rendered" >&2; exit 1
  fi
  grep -q "$expected" "$tmp/$name.err" || { echo "FAIL $name: unexpected error"; more "$tmp/$name.err"; exit 1; }
  echo "OK reject $name"
}
reject base-connection 'managed storage requires.*persistent' --set kubernetes.mode=connection
reject base-session 'managed storage requires.*persistent' --set kubernetes.mode=session
reject template-connection 'managed storage requires.*persistent' --set 'kubernetes.podTemplates[0].mode=connection'
reject template-session 'managed storage requires.*persistent' --set 'kubernetes.podTemplates[0].mode=session'
reject disabled-server 'managed storage requires configServer.enabled' --set configServer.enabled=false --set configserver.url=https://config.example.test
render -f "$tmp/managed-values.yaml" --set 'kubernetes.podTemplates[0].mode=persistent' >/dev/null
for size in null 0 0Gi -1Gi garbage 1Zi 0.000Gi; do
  reject "size-$size" 'explicit positive storage request' --set "kubernetes.podTemplates[0].volumeClaimTemplates[0].spec.resources.requests.storage=$size"
done
for size in 1 1Gi 0.5Gi 1e3 1m; do
  render -f "$tmp/managed-values.yaml" --set-string "kubernetes.podTemplates[0].volumeClaimTemplates[0].spec.resources.requests.storage=$size" >/dev/null
done
for name in '' Bad home.data -home home- "$(printf 'a%.0s' {1..64})"; do
  reject "claim-name-${name:-empty}" 'valid claim name' --set-string "kubernetes.podTemplates[0].volumeClaimTemplates[0].metadata.name=$name"
done
reject duplicate-claim 'duplicate claim name' \
  --set 'kubernetes.podTemplates[0].volumeClaimTemplates[1].metadata.name=home' \
  --set 'kubernetes.podTemplates[0].volumeClaimTemplates[1].spec.resources.requests.storage=1Gi'
# Names are scoped to a template, not globally across templates.
render -f "$tmp/managed-values.yaml" \
  --set 'kubernetes.podTemplates[1].name=other' \
  --set 'kubernetes.podTemplates[1].volumeClaimTemplates[0].metadata.name=home' \
  --set 'kubernetes.podTemplates[1].volumeClaimTemplates[0].spec.resources.requests.storage=1Gi' >/dev/null

render >"$tmp/unused.yaml"
ruby -ryaml -e '
  values = YAML.load_file(ARGV[0])
  template = values.dig("kubernetes", "podTemplates", 0)
  template["volumeClaimTemplates"] = []
  template["mode"] = "session"
  template["spec"]["volumes"][0]["persistentVolumeClaim"]["claimName"] = "operator-shared"
  File.write(ARGV[1], values.to_yaml)
' "$tmp/managed-values.yaml" "$tmp/unmanaged-values.yaml"
render -f "$tmp/unmanaged-values.yaml" --set kubernetes.mode=connection >"$tmp/unmanaged.yaml"
render -f "$tmp/managed-values.yaml" --set storageQuota.enabled=false >"$tmp/quota-disabled.yaml"
render -f "$tmp/managed-values.yaml" --set storageQuota.requestsStorage=750Gi --set storageQuota.persistentVolumeClaims=75 >"$tmp/quota-custom.yaml"
ruby -ryaml -e '
  root = ARGV[0]
  %w[managed unused unmanaged quota-disabled quota-custom].each do |name|
    docs = YAML.load_stream(File.read(File.join(root, "#{name}.yaml"))).compact
    quotas = docs.select { |d| d["kind"] == "ResourceQuota" }
    if %w[unused unmanaged quota-disabled].include?(name)
      abort "#{name}: unwanted quota" unless quotas.empty?
    else
      abort "#{name}: expected one quota" unless quotas.size == 1
      quota = quotas.first
      abort "quota must target session namespace" unless quota.dig("metadata", "namespace") == "containerssh-sessions"
      expected = name == "managed" ? {"requests.storage" => "500Gi", "persistentvolumeclaims" => "50"} : {"requests.storage" => "750Gi", "persistentvolumeclaims" => "75"}
      abort "#{name}: wrong quota limits" unless quota.dig("spec", "hard") == expected
    end
    pvc_rules = docs.select { |d| %w[Role ClusterRole].include?(d["kind"]) }.flat_map do |role|
      (role["rules"] || []).select { |r| (r["resources"] || []).include?("persistentvolumeclaims") }.map { |r| [role, r] }
    end
    if %w[unused unmanaged].include?(name)
      abort "unused install gained PVC permissions" unless pvc_rules.empty?
    else
      abort "only config-server may access PVCs" unless pvc_rules.size == 1 && pvc_rules[0][0].dig("metadata", "name") == "storage-test-containerssh-config-server"
      role, rule = pvc_rules.first
      abort "PVC permissions must be namespaced get/create only" unless role["kind"] == "Role" && role.dig("metadata", "namespace") == "containerssh-sessions" && rule["apiGroups"] == [""] && rule["verbs"].sort == %w[create get]
    end
    if name == "unmanaged"
      cm = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("data", "ubuntu.yaml") }
      tpl = YAML.load(cm.fetch("data").fetch("ubuntu.yaml"))
      abort "unmanaged claim reference changed" unless tpl.dig("kubernetes", "pod", "spec", "volumes", 0, "persistentVolumeClaim", "claimName") == "operator-shared"
      abort "empty declarations must not leak into template" if tpl.key?("volumeClaimTemplates")
    end
    abort "Helm must not precreate claims" if docs.any? { |d| d["kind"] == "PersistentVolumeClaim" }
    puts "OK #{name}: conditional quota and least-privilege PVC access"
  end
' "$tmp"
