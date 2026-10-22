# Revision 51: simplification rollout evidence

Date: 2026-10-22. Source: `289d8001515de693ff9bdefe1557bbfb2f3b5186`.
Operator: coding agent acting on the owner's explicit commit/push/build/rollout/live-test request.
Context: `container-ssh`; release/management namespace: `containerssh`;
backend namespace: `containerssh-sessions`; chart: `0.5.0`; revision: **50 → 51**.

This records selected checks, not blanket production acceptance. Current image references,
protected identities and unresolved gates are in [deployment status](deployment-status.md).

## Scope and safeguards

- Published source to `main`; successful GitHub Actions runs:
  [server images](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/39810450307) and
  [Settings image](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/39810450488).
- Exported full live user values/manifests and before/after inventories outside git in a
  permission-restricted directory. Credentials and rollback exports are not repository evidence.
- Changed only auth/config image tags and the Settings image passed to config-server, using
  registry-resolved immutable digests. Server-side dry-run rendering was necessary for Kata's
  existing-installation lookup; no installation-state object was created or modified.
- Compared baseline and candidate renders: only auth/config Deployments changed. Gateway pin,
  configuration, ordinary templates, host identity/Secret references, RBAC, networks and storage
  were preserved. Baseline manifest/render differences were test/post-delete hook resources.
- Used Helm upgrade with full reviewed values, `--atomic --wait --timeout 5m`; revision 51
  completed successfully without rollback. All three Deployments are Ready; gateway pod UID
  `849655c7-a998-4725-b078-acc2d22cf495` remained unchanged.
- Used existing enrolled key and strict preexisting `[localhost]:2222` host trust. A new local
  port-forward could not bind because that port was already occupied; tests used the existing
  trusted endpoint and correlated new Settings pods/images in the explicitly selected cluster.
  No existing local listener was stopped.
- No ordinary pod deletion, PVC deletion, storage migration, authentik mutation, disruptive
  fault injection or broad cleanup was performed.

## Results

| Gate | Result / evidence |
| --- | --- |
| Offline | **PASS** full `tests/check.sh`, including build/vet/race and chart suites; Markdown target and diff checks |
| Gateway consumer validation | **PASS** real merge/backend/security validators plus binary parsing, including Settings and retained-storage cases |
| Auth messages | **PASS** gateway stub-webhook and locally built real-auth-image/fake-authentik Docker checks; these are not live authentik fault tests |
| Published Settings image | **PASS** Docker PTY smoke against the immutable published image, including UI, API degradation, packaged agent and terminal behavior |
| Ordinary control | **PASS** enrolled-key exec before/after rollout and throughout live probes, same pod UID/spec and Kata runtime |
| Unknown template | **PASS** `ssh-template-rejection.py`: prompt Permission denied in **0.17 s**, exit 255 |
| SSH Settings policy | **PASS** selected `ssh-settings-policy.py` checks with Paramiko 3.5.1: PTY/shell/resize, env spoof rejection, explicit exec/SCP/subsystem/signal/forwarding denials, one session per transport, small screen, q and non-PTY failure |
| Lifecycle | **PASS** `ssh-settings-lifecycle.py`: killed probe-local OpenSSH client cleans up its Settings pod; actual control master retains pod after q, rejects another session, and cleanup follows transport exit |
| UI/API | **PASS** separate 120×32 terminal-emulator check: verified owner, ubuntu row and Live state; refresh; delete dialog storage warning; cancellation without pod deletion; q exits 0 |
| Settings pod/access | **PASS** observed published Settings digest, projected-token volume, scoped account, exact `get/delete` Role for `box-8bbfc143fa`, account automount false/pod true, read-only root, no RuntimeClass/owner cap label/PVC/hostPath mounts |
| Final cleanup | **PASS** no new Settings test pods remain; ordinary pod and retained PVC UIDs and specs unchanged |

Docker initially could not import the gateway's multi-platform index (`cannot overwrite digest`).
Validation succeeded using its registry-verified linux/amd64 child manifest
`sha256:c2b64c500211eca7aee1aa79bfbdf2399509637a49cd6857e52d5eea302ab13b`.
The deployed gateway's original index pin was not changed.

## Storage observation and acceptance limits

Before the rollout, Ubuntu already had pod UID `fd5b685e-6a6b-48ef-9adc-9e392f2a0028` and a
`/home` mount backed by Bound claim `box-8bbfc143fa-home`, UID
`740e4339-cd61-4ac1-956d-252802036fa7`. Both UIDs/specs survived this rollout and all probes.
The older Pending/unmounted record is superseded by observation, not by a migration performed here.
Prior-file migration, volume writability and destructive retention acceptance remain **NOT RUN**.

Also **NOT RUN**: two-real-owner isolation, actual management deletion/recreation, full ordinary
network isolation, independent token audience/expiry validation, ambient Calico authority review,
cap/quota exhaustion, live authentik/webhook faults, instrumented side-effect absence, startup/crash
fault fixtures and optional resize/exit stress. Terminal-emulator checks are not human visual review.
Mounted-token-backed Live UI data is not a substitute for those separate authorization/network gates.
