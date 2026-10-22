#!/usr/bin/env bash
# Render only the gateway config; callers supply chart, release, namespace and Helm flags.
render_config() {
  local chart="$1" release="$2" namespace="$3"
  shift 3
  helm template "$release" "$chart" --namespace "$namespace" \
    --show-only templates/configmap.yaml "$@" |
    ruby -ryaml -e '
      document = YAML.load_stream(STDIN.read).find { |item| item.is_a?(Hash) && item["kind"] == "ConfigMap" }
      abort "rendered ContainerSSH ConfigMap not found" unless document
      print document.fetch("data").fetch("config.yaml")
    '
}
