#!/bin/sh
# Run from an untouched checkout/extraction of the pinned upstream commit.
set -eu
patch=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/gateway.patch

git apply --check "$patch"
git apply --include='*_test.go' "$patch"
for regression in sshserver:TestSettingsSingleAcceptedStart \
    security:TestSettingsSignalPolicyIndependentOfShell \
    kubernetes:TestSettingsReadinessFailureRollback \
    kubernetes:TestSettingsPIDFramingAndResize; do
    package=${regression%%:*}
    test=${regression#*:}
    output=$(mktemp)
    if go test -mod=readonly "./internal/$package" -run "^$test$" -count=1 -timeout=30s >"$output" 2>&1; then
        printf 'ERROR: stock %s unexpectedly passed\n' "$regression" >&2
        rm -f "$output"
        exit 1
    fi
    # A compiler/toolchain failure is not evidence of a behavioral regression.
    if ! grep -q '^--- FAIL: TestSettings' "$output"; then
        cat "$output" >&2
        rm -f "$output"
        exit 1
    fi
    printf 'RED stock %s: expected regression failure\n' "$regression"
    grep -E '^--- FAIL:|Messages:' "$output"
    rm -f "$output"
done

git apply --exclude='*_test.go' "$patch"
go test -race -mod=readonly ./internal/sshserver ./internal/security ./internal/kubernetes \
    -run '^TestSettings' -count=1 -timeout=60s
go test -mod=readonly ./internal/security -count=1 -timeout=30s
printf 'GREEN patched regression tests and existing security suite\n'
