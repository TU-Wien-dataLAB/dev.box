# Issue #12 — local implementation validation

Date: 2026-10-22. Baseline: `91b4e579bc13b7c2fe0699718745ce91c8a253ab`.
Scope: [issue #12](https://github.com/TU-Wien-dataLAB/dev.box/issues/12),
[plan](durable-storage-plan.md). No release rollout was performed.

## Offline gates

| Seam | Result | Invocation / coverage |
| --- | --- | --- |
| Go modules | PASS | In `config-server`, `auth-server`, `settings-tui`, `tests/config-merge`: `go build ./...`, `go vet ./...`, `go test -race ./...` (merger module has no test files) |
| Storage webhook/API | PASS | Native specs, deterministic retained claims, unmanaged references, exact identity/label verification, foreign/terminating/owned claim rejection, AlreadyExists races, partial failure/retry, cap-before-creation, effective modes, explicit sizes, alternating/concurrent owners, no cached-spec mutation |
| Chart rendering | PASS | `tests/chart-{auth-rendering,template-authorization,service-selector,kata-rendering,persistent-rendering,settings-rendering,storage-rendering}.sh` |
| Real v0.6 merger / backend validators / gateway parser | PASS | `tests/chart-config-binary-validation.sh`, including actual rendered storage envelope → HTTP webhook response → real merge → binary |
| Helm lint | PASS | `helm lint charts/containerssh --set kubernetes.mode=connection --set auth.publicKey.webhook.url=https://auth.example.test` |
| Config-server image | PASS | `docker build --platform linux/amd64 -t config-server:issue12 config-server` |
| Settings image | PASS | `docker build --platform linux/amd64 -t dev-box-settings:issue12 settings-tui`; `python3 tests/settings-image-smoke.py --image dev-box-settings:issue12` — management flow, agent compatibility, terminal restoration, degraded/API failure paths |
| Gateway denial regression | PASS | `tests/gateway-error-messages.sh`; `python3 tests/auth-server-error-messages.py` (real Docker gateway/auth image, stub authentik, real OpenSSH) |
| Whitespace | PASS | `git diff --cached --check` |

Local image IDs (not registry digests or published rollout references):

- config-server: `sha256:8d01ddcc6ec0b0c4eab91ab8ea2b645735d73943d55313240b71b4bf60b2386e`
- settings-tui: `sha256:e6d2c703ad4bff3a1f54b99fd23e9de8e2afc8cf0069674ff009ff7061e83ea3`
- Binary-validation gateway `deploy` resolved locally to
  `ghcr.io/tu-wien-datalab/containerssh@sha256:26a437650224406af0be64bb5e8911c616f12a3296bc148ff2c5becacf13cfab`.
  Banner tests used the chart's existing PR #10 digest `96519c315ae7…` instead.

## Findings corrected during validation

- The real merger rejected the issue's abbreviated container example with
  `spec.0.image (missing image name)`: nonempty container slices replace the base slice, not
  element-wise merging. Examples now explicitly author image, container security and limits.
  No new merge behavior or storage-specific injection was added.
- Spec review found retention wording clipped at 80×3/4. Compact confirmation rebudgeting fixes
  it; rendering tests cover 80/200 columns at heights 2, 3, 4, 5, 6, 10 and 24. Normal width
  clipping still applies on terminals too narrow for the sentence itself.
- An initial full-suite PTY failure exposed historical-output matching: cancel/navigation/reopen
  could finish at the same popup before a renderer tick, legitimately producing no new bytes.
  The executable test now waits for a fresh intermediate list frame and fresh popup output.
  Five focused `go test -race ./... -run '^TestManagementListSelectDeleteAndRecreate$' -count=5`
  repetitions and the final complete suite passed. Production input behavior is unchanged.

## Review and live limits

Independent Standards and Spec reviews were run from the implementation baseline; Standards had
no violations/actionable smells, and the Spec warning-layout and PTY synchronization findings
were fixed and rechecked. Neither axis substitutes for rollout acceptance.

The [Kata/Cinder/fsGroup spike](durable-storage-spike-2026-10-22.md) passed before implementation.
Managed provisioning through the deployed webhook, real SSH/TUI delete→reconnect retention,
real quota exhaustion/counting, first-connect managed provisioning latency, live policy/lifecycle
regression and real two-owner acceptance are **NOT RUN**. They require compatible published
immutable images, reviewed values and separate rollout approval. The release and existing box
were not intentionally mutated; retained fixture resources were removed with UID preconditions.
