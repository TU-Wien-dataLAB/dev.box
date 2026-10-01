#!/usr/bin/env bash
# Build locally; pass --push instead of --load to publish an immutable-tag build.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
image="${1:?usage: build.sh IMAGE [--load|--push]}"
output="${2:---load}"
case "$output" in --load|--push) ;; *) echo 'Use --load or --push' >&2; exit 1 ;; esac
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
"$root/prepare-source.sh" "$tmp/source"
cp "$root/Dockerfile" "$tmp/Dockerfile"
# Do not send Git metadata to the image builder.
rm -rf "$tmp/source/.git"
patch_sha="$(shasum -a 256 "$root/patches/0001-authentication-methods.patch" | awk '{print $1}')"
docker buildx build --platform linux/amd64 --build-arg "PATCH_SHA256=$patch_sha" \
  --metadata-file "$tmp/metadata.json" --tag "$image" "$output" "$tmp"
printf 'Build provenance (pin the published manifest digest when deploying):\n'
python3 -m json.tool "$tmp/metadata.json"
