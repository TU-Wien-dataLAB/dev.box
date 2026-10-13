# Settings gateway patch — issue #9

## Why this custom gateway exists

`gateway.patch` changes only stock ContainerSSH v0.6.0:

- **One accepted start per SSH session channel:** shell, exec and subsystem share
  a mutex-protected gate. Only a successful backend start consumes it; failed
  starts remain retryable. Process exit does not reset it. The separate lock
  avoids deadlocking backend callbacks that synchronously send exit status.
- **Signal policy:** `OnSignal` consults `Signal.Mode`, not `Shell.Mode`, retaining
  the existing default/allow/deny logic.
- **Failed connection-pod readiness:** once CREATE succeeds, a readiness error
  triggers rollback before the creation retry loop loses the pod reference.
  Rollback uses a fresh `kubernetes.timeouts.podStop` context even if `podStart`
  has expired. Persistent/session creation behavior is unchanged.
- **Initial PTY resize:** connection/persistent execs report a four-byte agent
  PID without session mode's `--wait` echo. Read that PID immediately so the
  gateway can resize a terminal-dependent application before it produces any
  output. Session-mode PTYs retain their existing six-byte framing.

No auth-method advertisement changes, policy defaults, guest application, chart,
webhook, or unrelated upstream fixes are included. The start gate applies to all
session channels, not just the settings template.

## Build and validate locally

From the repository root (Docker/BuildKit required):

```sh
docker build --platform=linux/amd64 --progress=plain \
  -t containerssh-settings:dev containerssh-settings
CONTAINERSSH_IMAGE=containerssh-settings:dev tests/chart-config-binary-validation.sh
```

The second command uses the repository's chart/merge fixtures and validates the
real gateway with `--dump-config`; it needs Helm, Ruby, Go, and chart dependencies.
Alternatively, mount your already-rendered effective configuration:

```sh
docker run --rm --platform=linux/amd64 \
  -v "$PWD/effective-config.yaml:/etc/containerssh/config.yaml:ro" \
  containerssh-settings:dev --config /etc/containerssh/config.yaml --dump-config
```

Mount any files referenced by that configuration too (e.g. Kubernetes CA/token).
Do not use real credentials in smoke fixtures.

The Docker build runs `verify.sh`: apply tests alone, require **behavioral** failure
on stock at each SSH/security/Kubernetes seam, then apply production changes and
require green regression tests under `-race` plus the existing security suite.
The SSH regression uses real SSH request/reply transport; Kubernetes tests use
real clients/readiness watches against an HTTP API fixture, including DELETE
failure and unchanged persistent behavior. The PID regression couples the real
execution/PID reader to initial resize at the Kubernetes stream boundary, with
fragmented four-byte PID data and no application output; it also covers session
mode's wait/echo framing and non-PTY execution. No cluster is required. To repeat
without Docker, run `verify.sh` by absolute path from a fresh extraction of the
pinned source with Go and Git installed; it modifies that extraction only.

## Pins and use

- Upstream peeled `v0.6.0` commit: `76b43f44fce98315e7ab56c3db9d14248671a226`
  (not the adjacent fork's HEAD).
- Source tarball SHA-256: `bb4b83b8f7517581e738a6c1d7a12d9f8656fdd69e985f780b514438e8cdbf96`.
- Builder: Go 1.25.6/bookworm, digest pinned in `Dockerfile`; dependencies use the
  upstream, unchanged `go.mod`/`go.sum` with `-mod=readonly`.
- Runtime: official `containerssh/containerssh:v0.6` digest
  `sha256:7adf5995d6d43ecc95fe8225ee87e14d0f1996a03a890b3824d07f5c6a572d07`.
  This manifest is **linux/amd64 only** (emulation required on ARM hosts).

Only `/containerssh` is replaced: existing runtime packages, non-root UID/GID
1022, ports, volumes, entrypoint and CMD are inherited. Publish the resulting
image under an immutable identifier and select it as the existing release's
**gateway** image, not its guest image. Deployment requires separate approval;
this directory does not upgrade the release.

## Cleanup and validation limits

Rollback is bounded by a separate `podStop` budget per failed readiness attempt;
DELETE retries stop at that deadline and failures are logged. The original
readiness failure remains the startup error. There is no background cleanup
controller or later retry after rollback exhausts its budget.

**DELETE success means deletion accepted (or already NotFound), not confirmed
pod disappearance.** Stock disconnect cleanup has the same semantics. A healthy
API alone cannot guarantee disappearance: finalizers, grace periods and node/
controller failures can retain objects. The patch neither polls for disappearance
nor removes finalizers; operators must observe eventual removal separately.

Connection-mode cleanup normally runs on full SSH disconnect. TUI exit on a
multiplexed connection need not disconnect its parent. Local tests and config
validation do not prove the deployed application-only policy, actual cluster
cleanup timing, or ordinary persistent reconnects; those need the separate
end-to-end acceptance checks.

## Local validation record

Built `containerssh-settings:dev` (linux/amd64), local image ID
`sha256:f1aa766def6fc025495f4a2d0619b288dfef094d59f1681c6780a0a45a23797c`
(not a registry digest). Stock red / patched race-green checks, the existing
security suite, and the full upstream SSH suite passed. The shared config check
accepted seven effective configs via `--dump-config` and rejected unsafe
mixed-mode creation through the actual upstream merger/pod validator. Inspection
confirmed unchanged runtime layers/configuration plus one binary-replacement
layer. No live cluster validation or deployment was performed.
