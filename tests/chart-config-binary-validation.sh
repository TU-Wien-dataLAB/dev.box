#!/usr/bin/env bash
# Feed rendered and representative webhook-merged configs to the real
# ContainerSSH v0.6 binary for strict parsing/defaulting. The Go merger also
# calls the actual v0.6 pod/security validators (--dump-config itself does not).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
image="${CONTAINERSSH_IMAGE:-ghcr.io/tu-wien-datalab/containerssh:deploy}"
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

# Import the actual v0.6 structutils.Merge (nonzero-only mergo.WithOverride),
# including base defaulting. A Ruby map merge can incorrectly clear true values.
(cd "$repo_root/tests/config-merge" && go build -o "$tmp/config-merge" .)
merge_pod_override() {
  "$tmp/config-merge" "$1" "$2" "$3"
}

validate_config() {
  local label="$1"
  local config="$2"
  local stderr="$tmp/stderr-${label}.log"

  if ! docker run --rm \
    -v "$config:/etc/containerssh/config.yaml:ro" \
    -v "$tmp/token:/var/run/secrets/kubernetes.io/serviceaccount/token:ro" \
    -v "$tmp/ca.crt:/var/run/secrets/kubernetes.io/serviceaccount/ca.crt:ro" \
    "$image" --config /etc/containerssh/config.yaml --dump-config \
    >"$tmp/dump-${label}.json" 2>"$stderr"; then
    printf 'FAIL %s config validation:\n' "$label" >&2
    cat "$stderr" "$tmp/dump-${label}.json" >&2
    return 1
  fi

  if grep -q CORE_CONFIG_ERROR "$stderr"; then
    printf 'FAIL %s config produced CORE_CONFIG_ERROR:\n' "$label" >&2
    cat "$stderr" >&2
    exit 1
  fi
  ruby -ryaml -e '
    input = YAML.load_file(ARGV.fetch(0))
    dump = YAML.load_file(ARGV.fetch(1))
    expected = input.dig("kubernetes", "pod", "spec", "runtimeClassName")
    actual = dump.dig("kubernetes", "pod", "spec", "runtimeClassName")
    abort "ContainerSSH lost runtimeClassName: #{expected.inspect} -> #{actual.inspect}" unless expected == actual
  ' "$config" "$tmp/dump-${label}.json"
  if [[ "$label" == settings-effective || "$label" == settings-external-effective ]]; then
    ruby -ryaml -e '
      cfg = YAML.load_file(ARGV[0])
      pod = cfg.dig("kubernetes", "pod")
      abort "settings mode/creation incompatible" unless pod["mode"] == "connection" && !pod["createMissingPods"]
      abort "settings command changed" unless pod["shellCommand"] == ["/usr/local/bin/dev-box-settings"]
      policy = cfg.fetch("security")
      abort "force command lost" unless policy["forceCommand"] == "/usr/local/bin/dev-box-settings"
      abort "default deny lost" unless policy["defaultMode"] == "disable" && policy["maxSessions"] == 1
      %w[shell tty].each { |k| abort "#{k} permission lost" unless policy.dig(k, "mode") == "enable" }
      %w[command subsystem env signal].each { |k| abort "#{k} denial lost" unless policy.dig(k, "mode") == "disable" }
      abort "forwarding denial lost" unless policy.fetch("forwarding").values.all? { |v| v == "disable" }
      abort "scoped token policy lost" unless pod.dig("spec", "automountServiceAccountToken") == true && pod.dig("spec", "serviceAccountName").start_with?("dev-box-owner-")
      abort "settings must use default runtime" if pod.dig("spec", "runtimeClassName")
      abort "settings inherited base pod identity" if pod.dig("metadata", "name") || pod.dig("metadata", "labels", "dev.box/owner")
      abort "settings container floor lost" unless pod.dig("spec", "containers").size == 1 && pod.dig("spec", "containers", 0, "securityContext", "readOnlyRootFilesystem") == true
      abort "owner plumbing lost" unless pod.dig("spec", "containers", 0, "env").any? { |e| e["name"] == "DEV_BOX_AUTHENTICATED_OWNER" && e["value"] == "alice" }
      abort "box template plumbing lost" unless pod.dig("spec", "containers", 0, "env").any? { |e| e["name"] == "DEV_BOX_BOX_TEMPLATES" && e["value"] == "ubuntu" }
    ' "$tmp/dump-${label}.json"
  fi
  printf 'OK   real ContainerSSH binary accepts %s config and preserves RuntimeClass\n' "$label"
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
render_config connection --set kata.enabled=false >"$tmp/config-connection-no-kata.yaml"
validate_config connection-no-kata "$tmp/config-connection-no-kata.yaml"

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

# Use the actual synthesized HTTP response, not a duplicate YAML example.
(cd "$repo_root/config-server" && CONTAINERSSH_TEST_SETTINGS_RESPONSE="$tmp/settings-override.yaml" \
  go test -count=1 -run '^TestSettingsWebhookResponse$' .)
render_config persistent --set configServer.enabled=true >"$tmp/settings-base.yaml"

# Exercise the ordinary per-template Kata response as well as the shared base.
helm template binary-settings "$chart" --namespace containerssh \
  --set configServer.enabled=true \
  --set kubernetes.podTemplates[0].name=ubuntu \
  --set auth.publicKey.webhook.url=https://auth.example.test >"$tmp/settings-rendered.yaml"
ruby -ryaml -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  templates = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("metadata", "name").end_with?("-templates") }
  override = YAML.load(templates.dig("data", "ubuntu.yaml"))
  pod = override.dig("kubernetes", "pod")
  pod["createMissingPods"] = true
  pod["metadata"] = {"name" => "box-0123456789", "labels" => {"dev.box/owner" => "alice"}}
  abort "ordinary template lost Kata" unless pod.dig("spec", "runtimeClassName") == "kata-qemu-runtime-rs"
  File.write(ARGV[1], YAML.dump(override))
' "$tmp/settings-rendered.yaml" "$tmp/ordinary-override.yaml"
merge_pod_override "$tmp/settings-base.yaml" "$tmp/settings-override.yaml" "$tmp/settings-effective.yaml"
validate_config settings-effective "$tmp/settings-effective.yaml"
render_config persistent --set configserver.url=https://config.example.test >"$tmp/settings-external-base.yaml"
merge_pod_override "$tmp/settings-external-base.yaml" "$tmp/settings-override.yaml" "$tmp/settings-external-effective.yaml"
validate_config settings-external-effective "$tmp/settings-external-effective.yaml"
merge_pod_override "$tmp/settings-base.yaml" "$tmp/ordinary-override.yaml" "$tmp/ordinary-mixed-effective.yaml"
validate_config ordinary-mixed-effective "$tmp/ordinary-mixed-effective.yaml"

# Managed storage: rendered envelope -> real config webhook -> actual merger ->
# gateway parser. Claims are created against the offline Kubernetes fake only.
cat >"$tmp/storage-values.yaml" <<'YAML'
configServer:
  enabled: true
kubernetes:
  podTemplates:
    - name: ubuntu
      volumeClaimTemplates:
        - metadata: {name: home}
          spec:
            accessModes: [ReadWriteOnce]
            resources: {requests: {storage: 25Gi}}
      spec:
        securityContext: {fsGroup: 1000}
        volumes:
          - name: home
            persistentVolumeClaim: {claimName: home}
        containers:
          - name: shell
            image: containerssh/containerssh-guest-image
            securityContext:
              allowPrivilegeEscalation: false
              capabilities: {drop: [ALL]}
            resources: {limits: {memory: 128Mi, cpu: 500m}}
            volumeMounts: [{name: home, mountPath: /home}]
            env: [{name: HOME, value: /home}]
YAML
helm template binary-storage "$chart" --namespace containerssh \
  --set auth.publicKey.webhook.url=https://auth.example.test \
  -f "$tmp/storage-values.yaml" >"$tmp/storage-rendered.yaml"
ruby -ryaml -e '
  docs = YAML.load_stream(File.read(ARGV[0])).compact
  templates = docs.find { |d| d["kind"] == "ConfigMap" && d.dig("metadata", "name").end_with?("-templates") }
  File.write(ARGV[1], templates.fetch("data").fetch("ubuntu.yaml"))
' "$tmp/storage-rendered.yaml" "$tmp/storage-template.yaml"
(cd "$repo_root/config-server" && \
  CONTAINERSSH_TEST_STORAGE_TEMPLATE="$tmp/storage-template.yaml" \
  CONTAINERSSH_TEST_STORAGE_RESPONSE="$tmp/storage-override.yaml" \
  go test -count=1 -run '^TestStorageWebhookResponse$' .)
merge_pod_override "$tmp/settings-base.yaml" "$tmp/storage-override.yaml" "$tmp/storage-effective.yaml"
validate_config storage-effective "$tmp/storage-effective.yaml"
ruby -ryaml -e '
  pod = YAML.load_file(ARGV[0]).dig("kubernetes", "pod")
  c = pod.dig("spec", "containers", 0)
  abort "authored guest image lost" unless c["image"] == "containerssh/containerssh-guest-image"
  abort "authored HOME lost" unless c["env"].any? { |e| e["name"] == "HOME" && e["value"] == "/home" }
  abort "authored mount lost" unless c["volumeMounts"].any? { |m| m["name"] == "home" && m["mountPath"] == "/home" }
  abort "authored fsGroup lost" unless pod.dig("spec", "securityContext", "fsGroup") == 1000
  abort "symbolic claim leaked" unless pod.dig("spec", "volumes", 0, "persistentVolumeClaim", "claimName") == "box-ec061c6832-home"
' "$tmp/dump-storage-effective.json"
printf 'OK   managed storage preserves the authored guest image, native mount, HOME and fsGroup through the real merger\n'

# Regression: a true base createMissingPods cannot be cleared by a false/empty
# webhook field. Prove the real merger retains it and backend validation fails.
ruby -ryaml -e '
  cfg = YAML.load_file(ARGV[0])
  cfg.dig("kubernetes", "pod")["createMissingPods"] = true
  File.write(ARGV[1], YAML.dump(cfg))
' "$tmp/settings-base.yaml" "$tmp/unsafe-base.yaml"
if merge_pod_override "$tmp/unsafe-base.yaml" "$tmp/settings-override.yaml" "$tmp/unsafe-effective.yaml" >"$tmp/unsafe.log" 2>&1; then
  printf 'FAIL actual v0.6 validator accepted connection mode with leaked persistent creation\n' >&2
  exit 1
fi
grep -q 'createMissingPods can only be used with persistent execution mode' "$tmp/unsafe.log"
printf 'OK   actual v0.6 merger retains base true; pod validator rejects unsafe mixed-mode config\n'
