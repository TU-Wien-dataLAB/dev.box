# Issue #12 — durable storage live release acceptance

Date: 2026-10-22. Actor: repository owner via approved agent session. Approval:
**“ok rollout a release to test it live”**, following the implementation/local validation.
Context `container-ssh`, release `containerssh`, namespace `containerssh`, backend namespace
`containerssh-sessions`. Scope: immutable config-server/Settings images and an isolated temporary
`smoke-storage` template; preserve the real `ubuntu` box and gateway/auth pins.

## Release artifacts

| Artifact | Value |
| --- | --- |
| Implementation/image source | `b518b101a1c876f4cd730a281d90c57fb90612a9` |
| Config-server publish run | [39762740165](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/39762740165), success |
| Settings publish run | [39762740141](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/39762740141), success |
| Config-server pin | `ghcr.io/tu-wien-datalab/dev.box/config-server:sha-b518b10@sha256:d00398ba27e5500dfa258655830d6317952a5d1c8624aec82f1f91d4ec9a99d9` |
| Settings pin | `ghcr.io/tu-wien-datalab/dev.box/settings-tui:sha-b518b101a1c876f4cd730a281d90c57fb90612a9@sha256:6b791e3b6490d477df5a6cd27c359359e5d675bbd2793943311f29dac1513a50` |
| Gateway (unchanged) | `ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d` |
| Auth-server (unchanged) | `ghcr.io/tu-wien-datalab/dev.box/auth-server:sha-245b9e2@sha256:36e68dc2e1686378121a37aebff86ff4c28f42ffe127f9903bae2fe30c0e5f73` |
| Chart/revisions | `containerssh-0.5.0`, 47 → 48 (temporary storage template) → 49 (fixture removed) |

Images came from push-triggered CI only; no duplicate workflow dispatch. The auth image workflow
also ran, but its new image was **not** selected. Release chart defaults now pin the two verified
images; live values explicitly pin them independently.

## Preparation and preservation

- Exported original user values, manifests, pod/storage/deployment inventories to a restricted
  directory outside the repository. Rendered complete copied values, never `--reuse-values`.
- Compared user values structurally: revision 48 changed only config-server/Settings tags and
  appended the test template. Revision 49 differs from the original user values **only in those
  two image tags**. Original ordinary templates, cap 3, Secret references and credentials retained.
- Render comparison confirmed unchanged NetworkPolicy, Secret and Service objects except chart
  version labels. The existing ordinary pod was never deleted/recreated by this rollout.
- Baseline and final ordinary pod: `box-8bbfc143fa`, UID
  `33fb87e3-0678-4baa-ab6a-4b2acc0fedb1`, Kata guest kernel `6.18.35`.
- Published Settings image passed the complete Docker image smoke. Rendered configs/actual
  storage webhook responses passed the real merger, backend validators and the **release gateway
  digest** through `tests/chart-config-binary-validation.sh`. Helm lint passed with reviewed values.

Both upgrades used:

```bash
helm upgrade containerssh charts/containerssh --kube-context container-ssh \
  --namespace containerssh -f <restricted-reviewed-full-values.yaml> \
  --atomic --wait --timeout 10m
```

## Live storage acceptance (revision 48)

Test template: `smoke-storage`, one native `home` claim template, **1Gi** RWO, `csi-cinder`.
Authored pod fields: `fsGroup: 1000`, `/home` mount, `HOME=/home`, complete guest container image,
security settings and resource limits. Runtime injected as `kata-qemu-runtime-rs`.
Connections used the real gateway/auth/config-server and enrolled key `~/.ssh/slurm_tu_wien`;
verified owner `matthias.matt`, trusted `[localhost]:2222` host key, owned loopback port-forward.
Settings output was reconstructed with `pyte`, not raw-line substring parsing.

| Gate | Result / observation |
| --- | --- |
| First real SSH login/provision/write | PASS — 23.37 s (cold login through completed command, not a CSI-only timing); UID 1000, supplementary group 1000; `/home` owner 0/group 1000, mode `drwxrwsr-x`; kernel 6.18.35 |
| Deterministic managed identity | PASS — pod `box-ebc5bb9c84`, PVC `box-ebc5bb9c84-home`; exact owner/template/claim annotations verified, claim Bound with no owner references |
| Settings confirmation | PASS — title names only `smoke-storage` and its exact pod; full “Local disk data is destroyed; data on retained PVCs is kept.”, active-session and next-login consequences visible at 120×30 |
| Actual Settings deletion | PASS — `d` then guarded `y` deletes only the test pod via its projected owner credential; UI reaches not-created/reconnect hint; PVC remains with identical UID |
| Real SSH reconnect | PASS — 21.68 s through completed command; same deterministic name, **new pod UID**, same PVC UID and backing PV |
| File semantics | PASS — synced `/home/issue12-retained` marker recovered exactly, `/tmp/issue12-ephemeral` absent, mount still writable |
| Quota accounting | PASS — live quota hard 500Gi/50 PVCs; used 1Gi/1 claim |
| Real quota admission | PASS — server-side dry-run 501Gi PVC creation rejected Forbidden / exceeded quota; no negative-test object created |
| Config-server PVC permission | PASS — rendered/deployed Role contained only PVC `get/create`; no PVC list/delete grant |

Identities:

- First test pod UID: `d7601761-723c-4256-85e7-3479b4156a25`.
- Recreated test pod UID: `6a7dcae2-6257-430e-8a4f-9b24d8655f0d`.
- PVC UID: `0ec0c765-d7fb-46a5-bfbb-acc859f6bfe3`.
- Backing PV: `pvc-0ec0c765-d7fb-46a5-bfbb-acc859f6bfe3`.

## Final release and regression (revision 49)

The [sanitized final-state snapshot](durable-storage-final-state-2026-10-22.json) records the
queried Helm revision, exact remaining pod UID, absence of PVCs/quota/test PV, no PVC mounts in
`ubuntu`, and structural equality of its pre/post user template. It contains no credentials.
Cleanup method and UID-precondition checks below were observed in the agent's execution
transcript; separate raw DELETE response receipts were not retained.

- Deleted only the recorded test pod and then its PVC, using exact UID-preconditioned raw DELETE
  requests and wait-for-delete. The backing PV subsequently disappeared. Direct OpenStack volume
  inventory was not queried; PV disappearance alone is not independent cloud-side deletion proof.
- Removed only `smoke-storage` from the preserved values and upgraded to revision 49. Conditional
  quota and PVC grant disappear because there are now no managed claim templates. A fresh Settings
  request reconciled the owner Role back to pod `get/delete` on **only `box-8bbfc143fa`**.
- `tests/ssh-settings-policy.py`: **82 PASS**, exit 0. New Settings image observed in-pod;
  SSH env/exec/subsystem/signal/forwarding policy unchanged, one-session boundary, PTY/resize,
  transport cleanup and ordinary UID preservation passed.
- `tests/ssh-settings-lifecycle.py`: PASS, exit 0 — SIGKILL cleanup and actual OpenSSH control
  master retention/second-session refusal/exit cleanup; ordinary UID and Settings baseline intact.
- `tests/ssh-template-rejection.py`: PASS — unknown name rejected in **0.38 s**, Permission denied.
- `tests/settings-owner-authorization.py`: all selected checks PASS; get/delete own exact name
  permitted, foreign/settings names and broader pod/Secret/RBAC mutations denied. Explicit
  owner-SA `get persistentvolumeclaims` authorization check returned **no**.
- Final state: all three server deployments Ready on reviewed pins, only the original ordinary
  pod remains; no test Settings pods, PVCs or ResourceQuota remain in the session namespace.

## Limits and next decision

**The live `ubuntu` template is unchanged and has no managed PVC.** This release makes retained
storage available per-template; it does not migrate existing writable-layer data or mutate running
pods. Enabling `/home` storage for `ubuntu` requires a reviewed template change and explicit
pod recreation/data migration. Its existing files must not be discarded implicitly.

Two-real-owner acceptance remains issue #10. Live foreign/terminating-claim collision, partial
multi-claim failure and concurrent-owner scenarios were covered offline, not repeated against
production. Quota rejection above proves Kubernetes admission; real config-webhook denial on
quota exhaustion was not forced (offline API-error gates cover propagation). Resize/exit stress
was not selected. The [preimplementation compatibility spike](durable-storage-spike-2026-10-22.md)
and [local validation](durable-storage-local-validation.md) remain separate evidence.

Operational/test artifacts: the pre-upgrade port-forward briefly remained bound to the replaced
pod and failed an SSH banner read; replacing the owned forward restored normal connections.
An overlapping policy invocation was stopped and then rerun sequentially to completion after
storage-fixture cleanup. Neither is reported as a successful acceptance run or a product fault.
