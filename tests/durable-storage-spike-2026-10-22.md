# Issue #12 compatibility spike — 2026-10-22

## Scope

User approved isolated tests in Kubernetes context `container-ssh` before implementation.
This is a **direct Kubernetes pod/exec compatibility test**, not managed claim provisioning,
SSH gateway, settings deletion, quota, two-owner acceptance, or release rollout evidence.
No Helm values, release deployments, existing boxes or network policies were changed.

## Observed configuration

- Namespace: `containerssh-sessions`; fixture `issue12-spike-a8b32c4f`.
- Existing guest image: `containerssh/containerssh-guest-image` (the live ordinary pod uses this
  unpinned reference; the spike does not establish immutable guest-image provenance).
- Runtime: `kata-qemu-runtime-rs`; guest kernel `6.18.35`.
- StorageClass `csi-cinder`: provisioner `cinder.csi.openstack.org`, reclaim `Delete`,
  binding `WaitForFirstConsumer`, expansion allowed.
- CSIDriver fsGroupPolicy: `ReadWriteOnceWithFSType`.
- One operator-created 1Gi RWO claim; pod UID/GID/fsGroup 1000; mount `/home`, HOME `/home`.
  No owner references on the claim; no service-account token mounted in the test pod.

## Results

| Gate | Result | Observation |
| --- | --- | --- |
| Provision, attach, Kata readiness | PASS | First pod ready 15.73 s after start, including PVC creation |
| Non-root ownership/writability | PASS | `uid=1000 gid=1000 groups=1000`; `/home` mode `drwxrwsr-x`, owner 0, group 1000; marker write succeeded |
| Retention across pod deletion | PASS | Marker `retained-issue12` written and synced in `/home`; matched after recreation |
| Ephemeral data lost | PASS | Marker created in `/tmp` absent after recreation |
| New pod identity | PASS | First `aaed7b5a-ae48-4134-a59f-1dd66d9fd478`, second `3944365e-ee05-4ced-9c32-be9b0888e1fd` |
| Same claim | PASS | PVC UID `712734b7-cc02-422e-9d7c-e568fa3a97e6`, Bound, PV `pvc-712734b7-cc02-422e-9d7c-e568fa3a97e6` |
| Fixture cleanup | PASS | Exact test pod/PVC deleted with UID-preconditioned raw DELETE; wait-for-delete completed; namespace PVC inventory afterward empty |

Total fixture lifetime, including deletion/recreation and cleanup: 27.54 s. The normal
`box-8bbfc143fa` remained Running; post-spike observed UID
`33fb87e3-0678-4baa-ab6a-4b2acc0fedb1`. No pre-spike UID was captured, so this is not a
before/after UID assertion. Backing Cinder volume deletion was not independently polled after
PVC removal. No default-runtime fallback is indicated by this spike.

Managed creation/reuse, identity checks and cache isolation are separate offline gates. End-to-end
SSH/TUI retention and managed provisioning latency remain **NOT RUN**, pending immutable images
and separate rollout approval; follow [the plan](durable-storage-plan.md).
