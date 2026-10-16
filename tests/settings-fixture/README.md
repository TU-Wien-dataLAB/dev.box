# Build the disposable Settings acceptance fixture

This tooling verifies issue #9 failure paths; it is **not a management API or a
production guest image**. See [historical live results](../settings-fixture-validation.md)
and the current [migration/acceptance guide](../settings-code-migration.md) before any deployment.
The [issue #9 runbook](../settings-acceptance.md) is historical; its values-defined settings item
and no-token floor no longer describe the current chart.

## Validate and publish

1. Run the unprivileged local checks:

   ```bash
   (cd tests/settings-fixture && go test -race ./... && go vet ./...)
   ```

   Real-process tests are skipped on macOS. Their opt-in flag is for a disposable
   Linux build stage only: they create `/usr/local/bin/dev-box-settings` and refuse
   to overwrite an existing file. **Do not enable it on a workstation or cluster
   node.** The Dockerfile runs these tests before constructing the fixture image.

2. Publish only when a temporary acceptance run is approved:

   ```bash
   gh workflow run settings-acceptance-fixture.yml --repo TU-Wien-dataLAB/dev.box --ref main
   ```

3. Resolve the successful build's immutable digest and verify source revision,
   linux/amd64 platform, user `1000:1000`, and inherited direct TUI entrypoint.
   Pin that digest for temporary test deployment; a `sha-*` tag alone is mutable.

## Use within an approved fixture run

- For a separately approved image-upgrade test, change only `settings.image.repository/tag`
  to the pinned fixture reference, preserve ordinary values/Secret references, and wait until the
  deployed config webhook synthesizes the new image. Do not add a settings template item or
  override its code-owned policy. Hold an old connection open; compare
  old/new pod UIDs and imageIDs. Restore the original image afterwards.
- For an unexpected TUI crash, identify a newly created Settings test pod and
  confirm its exact UID and fixture image before administrative exec of
  `/usr/local/bin/settings-acceptance-helper kill-tui`. It requires exactly one
  process with executable `/usr/local/bin/dev-box-settings`, PID greater than 1,
  and matching real/effective/saved/filesystem UIDs. It never deliberately targets
  PID 1 or a process group. This assumes pidfd-capable Linux; Go may fall back on
  unsupported kernels, and concurrent same-UID exec is not isolated. Use only
  disposable, controlled test pods—not arbitrary hosts or ordinary dev boxes.
- For missing-owner delivery, run the helper's `config-proxy` command in a
  temporary management-namespace pod without credentials/token automount. Set
  `--owner` to the real canonical test owner and `--upstream` to the bundled
  config service. Route the gateway to the fixture's **`/config`** URL: it removes
  `authenticatedUsername` only for that owner on `settings`, forwarding other
  valid requests unchanged. The proxy is unauthenticated and must remain private
  to the temporary fixture; it is not an authorization boundary.
- Restore gateway routing before removing the fixture. Disconnect only test
  transports, verify their original pod UIDs disappear, and remove temporary
  management resources with UID preconditions. Verify original values and the
  ordinary pod UID after restoration.

The default image entrypoint is still the real Settings TUI; it contains no shell.
The helper is reachable only by explicitly configured test commands or trusted
administrative exec, which is outside the client-facing SSH boundary.
