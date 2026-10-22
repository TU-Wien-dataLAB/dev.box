#!/usr/bin/env bash
# Offline regression checks: no Docker daemon, credentials, or cluster required.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
for command in go helm ruby python3; do
  command -v "$command" >/dev/null || { printf 'Missing prerequisite: %s\n' "$command" >&2; exit 1; }
done
if [[ ! -f charts/containerssh/charts/kata-deploy-4.2.0.tgz ]]; then
  printf 'Install chart dependencies first: helm dependency build charts/containerssh\n' >&2
  exit 1
fi
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
for module in auth-server config-server settings-tui tests/config-merge; do
  printf '\n== %s ==\n' "$module"
  (cd "$module" && go build -o "$build_dir/" ./... && go vet ./... && go test -race ./...)
done
for check in tests/chart-*.sh; do
  [[ "$check" == tests/chart-config-binary-validation.sh ]] && continue
  printf '\n== %s ==\n' "$check"
  bash "$check"
done
# Syntax validation only: never invoke the opt-in live probes here.
python3 - <<'PY'
import ast
from pathlib import Path
for path in sorted(Path('tests').glob('*.py')):
    ast.parse(path.read_text(), filename=str(path))
print('PASS Python test syntax')
PY
printf '\nPASS offline checks (Docker and live acceptance not run)\n'
