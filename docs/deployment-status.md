# Recorded deployment status

**Last recorded: 2026-10-22. This is not a fresh cluster observation or deployment approval.**
Read this page before following [operations](operations.md). Update recorded state only from
approved evidence; preserve unresolved holds and distinguish PASS / FAIL / BLOCKED / NOT RUN.
Chart defaults do not override live values. Sensitive exports and credentials stay outside git.

## Current baseline and storage hold

- Context `container-ssh`; Helm release/management namespace `containerssh`; backend namespace
  `containerssh-sessions`; revision **50**, chart **0.5.0**. ClusterIP + approved local forward,
  ingress disabled; stable Secrets `containerssh-host-key` and `containerssh-authentik-token`.
- Ordinary template: `ubuntu`; verified owner `matthias.matt`; cap **3**. Ordinary runtime
  `kata-qemu-runtime-rs`; Settings uses default runtime and scoped owner credentials.
- `ubuntu` now declares **5Gi**, RWO, `csi-cinder`, claim `home`, mount `/home`, `HOME=/home`,
  `fsGroup: 1000` for **new pods only**. Namespace quota is 500Gi/50 claims; config-server PVC
  rights are get/create, not list/delete. The Pending claim already accounts for 5Gi/1 claim.
- **Retained PVC `box-8bbfc143fa-home`, UID `740e4339-cd61-4ac1-956d-252802036fa7`, is Pending
  and NOT mounted.** WaitForFirstConsumer has no consuming pod: existing `box-8bbfc143fa`,
  **UID `5f35a566-ed3f-4244-b88f-eb754daa229e`**, still has its old spec without a PVC mount.
  Revision 50 preserved that UID. **Do not delete/recreate this pod without explicit
  recreation/data-loss approval and a decision about its current writable-layer data.**
  The PVC does not copy existing files retroactively. This owner-requested claim is not a
  leftover fixture; no fixture cleanup approval applies to it.
- Template changes do not update existing pods or PVC specs. Retained PVC deletion is a
  separate deliberate operator decision; no pod/PVC deletes without approval. `csi-cinder`
  was recorded as WaitForFirstConsumer, reclaim Delete, expansion allowed. Retention is not
  backup protection against PVC/namespace deletion, storage failure or user file deletion.

## Recorded immutable release references

| Component | Reference |
| --- | --- |
| Gateway | `ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d` |
| Auth | `ghcr.io/tu-wien-datalab/dev.box/auth-server:sha-245b9e2@sha256:36e68dc2e1686378121a37aebff86ff4c28f42ffe127f9903bae2fe30c0e5f73` |
| Config | `ghcr.io/tu-wien-datalab/dev.box/config-server:sha-b518b10@sha256:d00398ba27e5500dfa258655830d6317952a5d1c8624aec82f1f91d4ec9a99d9` |
| Settings | `ghcr.io/tu-wien-datalab/dev.box/settings-tui:sha-b518b101a1c876f4cd730a281d90c57fb90612a9@sha256:6b791e3b6490d477df5a6cd27c359359e5d675bbd2793943311f29dac1513a50` |

The ordinary guest reference was `containerssh/containerssh-guest-image`, not digest-pinned;
immutable gateway/webhook/Settings provenance does not establish immutable guest provenance.
Gateway replacements must include the fork's Settings/lifecycle fixes and auth `message` support;
stock v0.6 and unpatched deploy reject the message field. Gateway source work follows AGENTS.md.

## Recorded authorization and network evidence

- Settings API egress targeted kube-system kube-apiserver pods. Calico post-NAT required
  443/6443; reverify actual topology for any new acceptance run.
- Ambient `calico-tiered-policy-passthrough` grants policy verbs in `projectcalico.org` through
  inherited groups. Downstream Calico authorization was not tested. Core pod negatives do not
  establish blanket least privilege or end-to-end network-policy isolation. Review before enrollment.
- Real management GET/DELETE through the TUI exercised the projected bearer and scoped API route,
  superseding the old static POC's API-egress block. This was not independent
  TokenReview/audience/expiry validation or a two-owner negative test.
- Fault-injection tooling and its publishing workflow are retired. Startup/crash/missing-owner
  historical evidence is not reproducible acceptance with current tooling.

## Unresolved gates

Historical tests are **not current acceptance**:

- **#10: two distinct real enrolled owners** concurrently rendering/reconnecting, real foreign
  get/delete negatives and retained-claim isolation remain pending; this gates pre-alpha enrollment.
  Synthetic owner metadata and fabricated foreign names do not complete it.
- **Ubuntu mounted-volume activation/migration remains pending** under the exact UID hold above.
  Managed SSH → Settings delete → reconnect retention passed on a disposable template previously;
  that is not ubuntu acceptance or permission to discard its local data.
- Live ambiguous-key fixtures, instrumented absence of arbitrary subprocess/file side effects,
  downstream inherited Calico authority and independent mounted-bearer audience/expiry review
  remain unproven; group denial is untested while not configured.
- Foreign/terminating-claim collisions, partial multi-claim failure and concurrent owners were
  covered offline, not by live production scenarios. Kubernetes quota admission was tested via
  server-side dry-run, but actual config-webhook denial at exhausted quota was not forced live.
- Startup/crash/missing-owner full-SSH fault gates have only retired-fixture historical evidence;
  optional resize/exit stress was not selected for the latest storage regression. Explicitly
  mark these NOT RUN until rerun with approved current tooling.
- **#13 metrics/top** remains follow-up; requests/limits in the UI are not measured usage.
