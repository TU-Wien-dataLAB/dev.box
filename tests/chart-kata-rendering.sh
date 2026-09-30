#!/usr/bin/env bash
# Kata dependency, backend RuntimeClass selection, opt-out, and template inheritance.
# Prerequisites: helm dependency build charts/containerssh; Helm and Ruby/YAML.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

render() {
  local name="$1"
  shift
  helm template kata-test "$chart" --namespace containerssh \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.test \
    --set authServer.authentik.tokenSecret=authentik-read-token \
    --set configServer.enabled=true "$@" >"$tmp/$name.yaml"
}

render default
render disabled --set kata.enabled=false
render connection --set kubernetes.mode=connection
render custom --set kubernetes.pod.spec.runtimeClassName=external-kata
render external --set kata.enabled=false --set kubernetes.pod.spec.runtimeClassName=external-kata
render templates \
  --set 'kubernetes.podTemplates[0].name=ubuntu' \
  --set 'kubernetes.podTemplates[0].metadata.labels.dev-box-template=ubuntu' \
  --set 'kubernetes.podTemplates[1].name=custom' \
  --set 'kubernetes.podTemplates[1].spec.runtimeClassName=external-kata'

ruby -ryaml -e '
  def docs(path)
    YAML.load_stream(File.read(path)).compact
  end
  def config(documents)
    document = documents.find { |d| d["kind"] == "ConfigMap" && d.dig("data", "config.yaml") }
    YAML.load(document.fetch("data").fetch("config.yaml"))
  end
  def checksum(documents)
    server = documents.find { |d| d["kind"] == "Deployment" && d.dig("metadata", "name") == "kata-test-containerssh" }
    server.dig("spec", "template", "metadata", "annotations", "checksum/config")
  end

  root = ARGV.fetch(0)
  %w[default disabled connection custom external templates].each do |name|
    path = File.join(root, "#{name}.yaml")
    documents = docs(path)
    enabled = !%w[disabled external].include?(name)
    classes = documents.select { |d| d["kind"] == "RuntimeClass" }
    expected_classes = enabled ? ["kata-qemu-runtime-rs"] : []
    abort "#{name}: incorrect RuntimeClasses" unless classes.map { |d| d.dig("metadata", "name") } == expected_classes
    if enabled
      klass = classes.first
      abort "wrong runtime handler" unless klass["handler"] == "kata-qemu-runtime-rs"
      abort "missing Kata-ready scheduling gate" unless klass.dig("scheduling", "nodeSelector", "katacontainers.io/kata-runtime") == "true"
      installer = documents.find { |d| d["kind"] == "DaemonSet" && d.dig("metadata", "name") == "kata" }
      abort "Kata installer missing" unless installer
      abort "system priority outside kube-system" if installer.dig("spec", "template", "spec", "priorityClassName")
    else
      abort "disabled Kata still renders resources/hooks" if File.read(path).include?("# Source: containerssh/charts/kata/")
    end

    pod = config(documents).dig("kubernetes", "pod")
    expected_runtime = case name
      when "disabled" then nil
      when "custom", "external" then "external-kata"
      else "kata-qemu-runtime-rs"
    end
    abort "#{name}: wrong backend runtime" unless pod.dig("spec", "runtimeClassName") == expected_runtime
    abort "#{name}: guest image lost" unless pod.dig("spec", "containers", 0, "image") == "containerssh/containerssh-guest-image"
    abort "#{name}: security defaults lost" unless pod.dig("spec", "securityContext", "runAsNonRoot") == true
    documents.select { |d| d["kind"] == "Deployment" }.each do |deployment|
      abort "server/webhook must not run in Kata" if deployment.dig("spec", "template", "spec").key?("runtimeClassName")
    end
    puts "OK   #{name}: installer and backend RuntimeClass"
  end

  templates = docs(File.join(root, "templates.yaml")).find { |d| d.dig("metadata", "name") == "kata-test-containerssh-templates" }
  ubuntu = YAML.load(templates.fetch("data").fetch("ubuntu.yaml"))
  custom = YAML.load(templates.fetch("data").fetch("custom.yaml"))
  abort "metadata-only template must inherit the base spec" if ubuntu.dig("kubernetes", "pod").key?("spec")
  abort "template runtime override lost" unless custom.dig("kubernetes", "pod", "spec", "runtimeClassName") == "external-kata"
  puts "OK   templates inherit the Kata base or explicitly override it"

  enabled_checksum = checksum(docs(File.join(root, "default.yaml")))
  disabled_checksum = checksum(docs(File.join(root, "disabled.yaml")))
  abort "Kata toggle must restart ContainerSSH with updated config" if enabled_checksum.to_s.empty? || enabled_checksum == disabled_checksum
  puts "OK   Kata toggle changes the generated-config checksum"
' "$tmp"
