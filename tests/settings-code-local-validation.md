# Settings as code — local validation (issue #11)

> **Follow-up, 2026-10-19:** the reviewed config-server image is now published and live migration
> and scoped-pod acceptance passed at release revision 41. See [live evidence](settings-code-live-validation.md)
> for current results and explicit token/network/Calico limitations. The local-only record below is historical.

Date: **2026-10-16**. Source: issue #11 implementation over
`d0a02e6f87019b80b6bb5252a2698496d814c636`, before the implementation commit.
Local checks only; **no live rollout, cluster mutations, SSH acceptance, or Kubernetes
API authorization acceptance were performed by this implementation session**.

The final complete suite passed from an isolated **staged-index export** (`git
checkout-index --all --prefix=<temporary-directory>/`, with the existing pinned Kata
archive copied in). This tested exactly the proposed commit: all five Go modules,
all seven chart matrices including real-binary validation, Helm lint and packaging.
The shared working-tree rerun had meanwhile failed in concurrently edited
`auth-server/template_authorization_test.go` message assertions. Those unrelated
unstaged auth changes were preserved and excluded from the passing snapshot.

## Passing checks

All commands below exited **0** unless explicitly marked blocked.

- In each of `config-server`, `auth-server`, `settings-tui`, `tests/config-merge`, and
  `tests/settings-fixture`: `go build ./...`, `go vet ./...`, `go test -race ./...`.
  The merge adapter has no Go test files. The complete Go regression run passed;
  unrelated concurrent auth-server edits are not part of this implementation commit.
- All existing Helm matrices: `tests/chart-auth-rendering.sh`,
  `tests/chart-template-authorization.sh`, `tests/chart-service-selector.sh`,
  `tests/chart-kata-rendering.sh`, `tests/chart-persistent-rendering.sh`, and
  `tests/chart-settings-rendering.sh`.
- `helm lint charts/containerssh --set kubernetes.mode=connection
  --set auth.publicKey.webhook.url=https://auth.example.test` and
  `helm package charts/containerssh -d /tmp/sshtest`.
- `bash -n` on every `tests/chart-*.sh`; Python AST parsing for `tests/*.py`;
  `python3 tests/settings-owner-authorization.py --help`; `git diff --check` on
  implementation changes. The authorization probe was **not** run against an API.
- `tests/chart-config-binary-validation.sh`: actual webhook HTTP response, actual
  v0.6 defaulting/mergo, security/backend validators, and real gateway `--dump-config`.
  Covers connection/persistent bases, ordinary persistent and connection overrides,
  synthesized settings with bundled **and external** webhooks, ordinary per-template
  Kata, and deliberate leaked-creation rejection. Explicit settings denial modes,
  owner env, single container, scoped service account/token floor and absent settings
  RuntimeClass survive the merge. This is config validation, not SSH policy acceptance.
- The binary suite passed against `ghcr.io/tu-wien-datalab/containerssh:deploy` and
  the additional immutable fork image
  `ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d`.
  `docker pull ghcr.io/tu-wien-datalab/containerssh:deploy` resolved registry digest
  `sha256:26a437650224406af0be64bb5e8911c616f12a3296bc148ff2c5becacf13cfab`.
- `python3 tests/settings-image-smoke.py --image dev-box-settings:issue9-local`:
  existing unchanged guest image, local image ID
  `sha256:9c4a6ca09426256c983951a6b961d3d26242f6b10cb651b1c2cee1c1946e8e67`
  (**not** a registry digest). Passed render/resize/q/terminal restoration, inert
  input, blank-owner/no-PTY failures, Unicode sanitization, shell absence, non-root
  entrypoint, agent console/PID/env and idle INT/TERM checks. Guest source/image was
  not changed or rebuilt for issue #11.

## Red/green and review evidence

- File-free settings handler test initially failed with `no pod template matches
  SSH username "settings"`; it passes with code synthesis.
- Scoped-access handler test initially failed with `settings has no per-owner
  projected token`; it passes with reconciliation and token plumbing.
- Helm red/green slices cover reserved-name rejection, unconditional safe-base and
  nested security-context restrictions, image wiring, allowlist and per-template Kata.
- The external-server creation regression initially failed because its base still
  rendered `createMissingPods: true`. The shared base now omits that flag in every
  generated configuration; external Helm and actual-response merge regressions pass.
- Independent Standards review: **0 actionable findings**. Independent Spec review:
  found the external-server creation leak; follow-up independently reproduced the
  corrected merge and reported **0 remaining findings**. Neither reviewer performed
  live operations.

Offline Kubernetes client fixtures cover deterministic/stable owner objects,
concurrent identities, exact own persistent names, excluded settings/nonpersistent
names, additions/removals, zero templates producing **zero rules**, drift repair,
conflict retries, malformed templates, API get/create/update failures and unexpected
immutable RoleBinding references. Fake clients do **not** enforce Kubernetes RBAC.

## Blocked image build

`docker build -t config-server:issue11-local config-server` exited **1** during Go
compilation with Docker builder **`no space left on device`**. No resulting image is
claimed; no Docker prune/cache/image cleanup was performed. Native build/vet/race
checks passed. Free Docker storage with operator approval, then rebuild/publish an
immutable config-server image before migration. A fresh Settings image build was
not attempted; regression used the existing unchanged guest image above.

## Live gates still pending

Follow [the migration and staged acceptance guide](settings-code-migration.md) only
after separate approval. Real API anti-escalation/binding behavior, owner authorization
positive/negative probes, projected mount/token authentication, actual SSH policy and
lifecycle/ordinary-box UID/cap regressions remain **NOT RUN** for issue #11.
Unchanged NetworkPolicy blocks settings-origin cluster API requests: distinguish this
from operator impersonation and mounted-token validation. Two real enrolled owners
remain tracked separately in #10. No management actions were added to the TUI.
