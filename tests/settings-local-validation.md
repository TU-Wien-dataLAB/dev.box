# Settings POC — local validation record

> **Historical issue #9 evidence.** Results below cover the values-defined, inherited-Kata,
> no-token POC, not issue #11's synthesized settings/scoped-token contracts. See
> [the new migration and staged plan](settings-code-migration.md); these recorded results are unchanged.

Issue: [#9](https://github.com/TU-Wien-dataLAB/dev.box/issues/9).
Local checks initially completed 2026-10-12; final PID/resize regression and
revalidation completed 2026-10-13. **Not live acceptance or deployment approval**.

## Passed

- Go build, vet, race tests, and module verification: `config-server`,
  `auth-server`, `settings-tui`, and test-only `tests/config-merge`.
- All six existing Helm matrices plus `tests/chart-settings-rendering.sh`;
  Helm lint and packaging of chart `0.3.2` (`appVersion: 0.6`).
- Settings webhook verified-owner injection, concurrent owner isolation,
  missing-owner denial, disposable mode, and exemption from persistent quota.
  Existing authentication/group/template policies and persistent tests pass.
- Actual v0.6 `structutils.Merge` plus pod/security validators; seven rendered
  base/effective configs parsed/defaulted by stock and patched `--dump-config`.
  A base `createMissingPods: true` survives the real merger and is correctly
  rejected by the pod validator when a template switches to connection mode.
  `--dump-config` alone does not perform factory/backend validation.
- Docker Settings image: `dev-box-settings:issue9-local`, linux/amd64,
  local image ID `sha256:9c4a6ca09426256c983951a6b961d3d26242f6b10cb651b1c2cee1c1946e8e67`.
  `tests/settings-image-smoke.py` passed terminal rendering, resizing, inert
  command/escape/suspend input, owner-control sanitization, missing-owner and
  non-PTY failure, quit/terminal restoration, no `/bin/sh`, and official-agent
  console/PID/environment plus idle INT/TERM behavior. Owners were synthetic.
  Executable PTY regression also verifies delayed initial dimensions succeed
  while a permanently zero-sized terminal fails within a two-second wait.
- Docker patched v0.6 gateway: `containerssh-settings:dev`, linux/amd64,
  local image ID `sha256:f1aa766def6fc025495f4a2d0619b288dfef094d59f1681c6780a0a45a23797c`.
  Stock-red/patched-race-green start, signal, readiness-rollback, and coupled
  PID/initial-resize tests (session wait/echo framing retained);
  existing upstream SSH and security suites. See its README for source/image pins.
- SSH acceptance probe syntax/help and offline request-packet/reply checks.
  These are **not** endpoint-policy evidence.

## Blocked / not run

- Rebuilt config-server Docker image: the existing Dockerfile reached Go
  compilation but failed with `no space left on device`. Docker Desktop's
  58.4 GiB filesystem was full. No unrelated images, caches, or volumes were
  pruned. Source build/tests passed; retry the image build before rollout.
- Full SSH → authentik → config webhook → Kubernetes → TUI behavior, two real
  owners, request denials, settings pod UIDs/images/cleanup, abrupt/control-master
  disconnect, least-privilege inspection, cap-boundary regression, image upgrade,
  startup/crash fixtures, and ordinary persistent pod UID preservation: **NOT RUN**.
- Registry publication, existing development-release upgrade, and all live
  observations: **NOT RUN**, awaiting separate explicit approval.

Follow [settings-acceptance.md](settings-acceptance.md) after resolving the build
blocker and obtaining approval. Local image IDs are not registry digests; pin
published rollout images immutably. This change does not complete parent #7.
