#!/usr/bin/env bash
# Materialize the exact upstream revision and apply the reviewable core patch.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=containerssh-server/source.env
source "$root/source.env"
destination="${1:?usage: prepare-source.sh NEW_DIRECTORY}"
if [[ -e "$destination" ]]; then
  printf 'Refusing to overwrite %s\n' "$destination" >&2
  exit 1
fi
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git init -q "$tmp/source"
git -C "$tmp/source" fetch -q --depth=1 \
  "${CONTAINERSSH_SOURCE_REPO:-https://github.com/ContainerSSH/ContainerSSH.git}" \
  "$CONTAINERSSH_SOURCE_REVISION"
git -C "$tmp/source" checkout -q --detach FETCH_HEAD
[[ "$(git -C "$tmp/source" rev-parse HEAD)" == "$CONTAINERSSH_SOURCE_REVISION" ]]
git -C "$tmp/source" apply --check "$root/patches/0001-authentication-methods.patch"
git -C "$tmp/source" apply "$root/patches/0001-authentication-methods.patch"
mv "$tmp/source" "$destination"
printf 'Prepared %s at %s + authentication-methods patch\n' "$destination" "$CONTAINERSSH_SOURCE_REVISION"
