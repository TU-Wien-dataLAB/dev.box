#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/charts/containerssh"
release="selector-test"
namespace="containerssh"

helm template "$release" "$chart" --namespace "$namespace" \
  --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.test \
  --set authServer.authentik.tokenSecret=authentik-read-token \
  --set configServer.enabled=true |
  ruby -ryaml -e '
    documents = YAML.load_stream(STDIN.read).compact
    fullname = "selector-test-containerssh"

    main_deployment = documents.find { |item| item["kind"] == "Deployment" && item.dig("metadata", "name") == fullname }
    main_service = documents.find { |item| item["kind"] == "Service" && item.dig("metadata", "name") == fullname }
    abort "main Deployment not found" unless main_deployment
    abort "main Service not found" unless main_service

    component = "app.kubernetes.io/component"
    abort "main pod is not labelled as the server component" unless main_deployment.dig("spec", "template", "metadata", "labels", component) == "server"
    abort "main Service does not select only server pods" unless main_service.dig("spec", "selector", component) == "server"

    auth_service = documents.find { |item| item["kind"] == "Service" && item.dig("metadata", "name") == "#{fullname}-auth-server" }
    config_service = documents.find { |item| item["kind"] == "Service" && item.dig("metadata", "name") == "#{fullname}-config-server" }
    abort "auth Service selector changed unexpectedly" unless auth_service.dig("spec", "selector", component) == "auth-server"
    abort "config Service selector changed unexpectedly" unless config_service.dig("spec", "selector", component) == "config-server"
  '

printf 'OK   main SSH Service selects only ContainerSSH server pods\n'
