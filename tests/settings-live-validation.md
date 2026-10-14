# Settings POC — live deployment evidence

**Historical revision-20 evidence.** Current gateway selection and the
resize-shutdown retest are in [revision-21 evidence](settings-fork-validation.md).

Status: **deployed and ready for interactive testing**, not full issue #9
acceptance. User-approved rollout and live checks completed on 2026-10-13.
Remaining gates are in [the acceptance runbook](settings-acceptance.md).

## Release

| Field | Observed value |
| --- | --- |
| Cluster context | `container-ssh` |
| Release / namespace | `containerssh` / `containerssh` |
| Helm revision | **20**, upgraded atomically from 19 |
| Chart / upstream version | `containerssh-0.3.2` / ContainerSSH v0.6 |
| Source commit | `4285f7893a1851eecf6eeec177a9964ef6815d3a` |
| Registered templates | Existing `ubuntu`, appended `settings` |
| Settings lifecycle | Connection-mode disposable pod, one session per transport |
| Ordinary lifecycle | Persistent; existing pod retained |
| Auth policy | Existing authentik public-key auth and template gate; group gate not configured |
| Host key / authentik token | Existing Secrets `containerssh-host-key` / `containerssh-authentik-token` |
| Exposure | Existing ClusterIP Service; no ingress added |

All three Deployments are Ready with zero restarts after rollout. Existing
user values were preserved except for the gateway/config-server image selections
and the appended Settings template. The applied values equal the reviewed values.
Auth-server image, ordinary template, Kata DaemonSet/RuntimeClass specs, network
policy, Services, Secret references, and namespace were preserved.

## Published and deployed images

Registry prefix: `ghcr.io/tu-wien-datalab/dev.box/`.

| Component | Immutable tag | Digest / observed imageID |
| --- | --- | --- |
| Gateway `containerssh-settings` | `sha-4285f7893a1851eecf6eeec177a9964ef6815d3a` | `sha256:b24020265e1171a5e47060e2d4e8298e125ca51e1cf43749352de4f8a270b2bb` |
| Guest `settings-tui` | `sha-4285f7893a1851eecf6eeec177a9964ef6815d3a` | `sha256:addad96cb660725389df3cf602d5b183db6864c15e60ae1766f6d768cf614252` |
| Config-server | `sha-4285f78` | `sha256:18bccdceceff387c8438a35297f640a8f9110de01dd2a7c620ccde18d866befe` |
| Auth-server (unchanged) | `sha-72cadaa` | `sha256:6f1a71ff5c46cc3d88ae967baf9641b057127bddb664118da0fb1644f4fc6baa` |

Gateway, guest, and config-server references include both tag and digest. Anonymous
registry pull checks passed. Publication succeeded in GitHub Actions:
[Settings/gateway run](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/38394996561)
and [config-server run](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/38394996588).
This supplied the rebuilt config-server despite the earlier local Docker disk
exhaustion; no unrelated Docker data was pruned.

## Live results

The automated probe used Paramiko **3.5.1**, the existing enrolled key, the
independently established `[localhost]:2222` host identity, a 90-second
per-operation bound, and a 1,800-second total bound. Kubernetes observations used
explicit context/namespace and Settings-only selector `dev.box/application=settings`.

| Gate | Result / evidence |
| --- | --- |
| Ordinary positive controls | PASS: `ubuntu` exec exited 0 before, during, and after acceptance checks |
| Ordinary pod preservation | PASS: `box-8bbfc143fa`, UID `b2abba32-495f-4212-a16c-b2490144789c`, Running before/after; never deleted/recreated |
| Settings startup / identity | PASS: PTY + shell ACK; Settings, POC/read-only, canonical owner `matthias.matt` (not login `settings`), and quit hint rendered |
| Session limit / repeated start | PASS: second session rejected with code **4** while active and after `q` on the same transport; repeated shell request returned failure |
| Environment / signal policy | PASS: owner, original-command, PATH, ENV, BASH_ENV spoof requests and signal requests returned explicit failure |
| Exec / file-transfer policy | PASS: exec with/without PTY, shell/app command texts, SCP forms, SFTP and arbitrary subsystem returned explicit failure |
| Forwarding policy | PASS: TCP/SOCKS underlying channels and Unix-socket channels rejected with code **1**; reverse TCP/Unix, X11 and agent requests returned explicit failure |
| Non-PTY behavior | PASS: helpful PTY explanation and application exit 1 |
| Input / size behavior | PASS: 40×12 startup, resize, inert typed commands, ESC, Ctrl-C and Ctrl-Z; subsequent `q` exited 0 |
| Parent transport lifetime | PASS: after `q`, parent transport and pod remained; second session still denied |
| Disconnect / fresh pods | PASS: full disconnect removed each observed Settings UID; next connection used a new UID; no test Settings pods remained |
| Real OpenSSH client | PASS: PTY startup, small terminal, resize, inert command/ESC/Ctrl-Z input, `q` exit 0, operational terminal attributes restored |
| Unknown template rejection | PASS: `settings-unregistered-probe` and absent `default` denied in 0.20 s with `Permission denied` / exit 255; working ordinary control retained |
| Effective guest inspection | PASS for inspected fields: UID/GID 1000, non-root, RuntimeDefault seccomp, read-only root, all capabilities dropped, no privilege escalation, resource limits, Kata runtime, idle agent command, trusted-owner env, no volumes/token/host namespaces |
| RBAC inspection | Core resource `get` denied for pods, secrets, configmaps, deployments, roles and rolebindings. Inherited Calico passthrough caveat below; no blanket management-RBAC denial claimed |

Observed happy-path Settings UIDs, both removed after full disconnect:

- `settings-k54rj`: `5ab14a4c-0d61-491b-8c5e-8bebd5b985e0`.
- `settings-sl7bm`: `8bf5bcfa-e73d-421f-bc52-b6ac9cd41135`.

The final real-OpenSSH inspection used `settings-5gzlj`, UID
`928cc9ce-b4ed-4008-abbc-ce7ad831cf08`, also removed afterwards. Its imageID matched
the published Settings digest and its runtime was `kata-qemu-runtime-rs`.

### Inspection caveats

The namespace's default service account receives the cluster's existing
`calico-tiered-policy-passthrough` ClusterRoleBinding through `system:authenticated`.
The rules list policy verbs in `projectcalico.org`; the service account cannot
`get tiers.projectcalico.org`. The aggregated API is `calico-system/calico-api`.
No binding was added/changed, no token or volume is mounted into Settings, and
end-to-end Calico tier authorization was **not tested**. Any cluster-wide RBAC
hardening needs separate scope/approval.

The first OpenSSH harness was inconclusive: it did not model a user's persistent
controlling-terminal session and timed out. A corrected wrapper and continuous
PTY draining verified quit. The initial exact termios comparison also treated
macOS's kernel-managed `PENDIN` bit as an application failure; after normalizing
only that bit, all operational terminal attributes matched. No application or
gateway change was needed. This was automated real-OpenSSH PTY evidence, not a
human visual inspection or proof of arbitrary subprocess/file-side-effect absence.

## Not run / not accepted

- Concurrent distinct-owner screens/reconnect: no second enrolled identity supplied.
- Ambiguous/rejected-key and group-policy fixtures; group gate is not configured.
- Missing verified identity through an isolated live webhook fixture.
- Deliberate missing executable, crash, abrupt-client-loss, or alternate-image upgrade gates.
- Actual OpenSSH control-master lifetime gate (Paramiko parent lifetime was tested).
- Persistent-box cap boundary with an isolated owner/fixture.
- Human visual/terminal check and instrumented absence of subprocess/file side effects.
- End-to-end Calico authorization review.

No production authentik records, ordinary pods, or unrelated cluster permissions
were changed. Issue #9 remains open for the outstanding acceptance gates.

## Evidence location

Local restricted snapshots/logs: `/tmp/dev-box-settings-rollout.WxaiGx/`.
Includes reviewed/applied values and manifests, before/after pod/deployment data,
`ssh-policy.log`, `openssh-smoke-final.log`, template-denial logs, and the selected
inspected-pod/permissions records. These temporary snapshots are not committed;
no private keys, tokens, or Secret contents are included in this report.
