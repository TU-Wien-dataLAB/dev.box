#!/usr/bin/env bash
# Run upstream SSH regressions plus the live-client harness against local fixtures.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
source_dir="${1:-$tmp/source}"
harness="$source_dir/internal/authintegration/devbox_advertisement_test.go"
harness_created=false
cleanup() {
  if [[ "$harness_created" == true ]]; then rm -f "$harness"; fi
  rm -rf "$tmp"
}
trap cleanup EXIT
if [[ $# == 0 ]]; then
  "$root/containerssh-server/prepare-source.sh" "$source_dir"
fi
if [[ -e "$harness" ]]; then
  echo "Refusing to overwrite $harness" >&2
  exit 1
fi
harness_created=true
cp "$root/tests/ssh-advertisement-harness_test.go" "$harness"
cd "$source_dir"
SSH_ADVERTISEMENT_SCRIPT="$root/tests/ssh-auth-advertisement.py" go test ./internal/authintegration -count=1 -timeout 2m
go vet ./internal/authintegration ./internal/sshserver ./internal/auditlogintegration ./internal/metricsintegration ./internal/security
