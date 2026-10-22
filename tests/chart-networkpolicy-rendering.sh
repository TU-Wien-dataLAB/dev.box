#!/usr/bin/env bash
# Session isolation and the settings-only API egress exception.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

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
