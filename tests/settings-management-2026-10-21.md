# Issue #7 settings management UI — live rollout and acceptance (2026-10-21)

Scope: [dev.box issue #7](https://github.com/TU-Wien-dataLAB/dev.box/issues/7) — the settings TUI
grown into the live management interface, deployed and accepted on the real cluster per
[the staged plan](settings-management-migration.md). Follow-up issues remain open: #10 (two real
owners, gates pre-alpha), #12 (PVC), #13 (metrics/top).

Approval and actor: rollout requested and approved by the repository owner on 2026-10-21
("can you deploy?"); all steps below ran with that approval on context `container-ssh` via an
owned loopback port-forward (`svc/containerssh` on local port 32222) and the enrolled key
`~/.ssh/slurm_tu_wien`. Host verification used the previously trusted `[localhost]:2222` public
host key, exposed as `[localhost]:32222` (same key, alias entry added to `~/.ssh/known_hosts`).

## Artifacts

| Artifact | Value |
| --- | --- |
| Rollout commit | `901d8a7eb8092db87b4fbd44fbed64beb74c1de3` ("Grow the settings TUI into the live box management UI (#7)") |
| Chart | `containerssh-0.4.0` (local), revisions 42 → 43 → 44 |
| settings-tui image | `ghcr.io/tu-wien-datalab/dev.box/settings-tui:sha-901d8a7eb8092db87b4fbd44fbed64beb74c1de3@sha256:08de4f9e7d47a5928b147591e4349bbb0183483920bbcd29fd13d2730841c1f2` |
| config-server image | `ghcr.io/tu-wien-datalab/dev.box/config-server:sha-901d8a7@sha256:db773cd25ef7598fc89197b03d4f27add749ae577f6bdc626a9f944e3c59497a` |
| Gateway image (unchanged) | `ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d` |
| Auth-server image (unchanged) | `ghcr.io/tu-wien-datalab/dev.box/auth-server:sha-245b9e2@sha256:36e68dc2e1686378121a37aebff86ff4c28f42ffe127f9903bae2fe30c0e5f73` |

Image publishing: the push of `901d8a7` to `main` did **not** trigger the image workflows (no runs
appeared for the head SHA; cause uninvestigated), so both images were published via explicit
`workflow_dispatch` on `main` (run `39621602637` config-server, run `39621607155` settings; both
success). The rollout pins digests in user values; the chart default was updated afterwards.

Cluster topology for the new egress policy: kubeadm-style control plane — `kube-apiserver-*` pods
in `kube-system` labeled `component=kube-apiserver` (verified). The chart's default
`networkPolicy.settingsApiEgress` selector therefore matches; no `ipBlock` override was needed.
Ordinary pods' policy is unchanged.

## Rollout

- Revision 42 (chart 0.4.0, both image pins): config-server deployment rolled to
  `sha-901d8a7@sha256:db773cd2…`; gateway and auth-server deployments unchanged (same digests);
  all Ready. Existing persistent pod untouched.
- Revision 43: temporary ordinary template `smoke-mgmt` added for the delete/recreate stage
  (allowlist became `["ubuntu","smoke-mgmt","settings"]`), removed again in revision 44
  (allowlist back to `["ubuntu","settings"]`). Revisions 43/44 kept all image pins and the host
  key/authentik Secret references.
- User values used for all three upgrades: the exported release values with the two new image pins
  (recorded in the table above); no other value changed.

## Gates

| Gate | Result / evidence |
| --- | --- |
| Local suites before rollout | PASS: all Go modules build/vet/test `-race`; all six chart rendering matrices; real-binary config validation on the pinned PR #10 gateway; Docker build + full settings image smoke (commit `901d8a7`, recorded in the issue) |
| Live UI render | PASS: `ssh -t settings@…` renders `dev.box settings`, `Owner: matthias.matt`, `Connection: Live`, the `ubuntu` row with live data (`Running`, up 21d3h, `containerssh/containerssh-guest-image`, `kata-qemu-runtime-rs`, restarts 0, `128Mi/500m`) |
| Live refresh | PASS: manual `r` plus two ~3 s poll cycles keep the UI alive; rows re-render on change |
| Delete dialog | PASS: `d` shows the dialog naming the box and all three consequences (local disk destroyed, active sessions killed, fresh box on next login); `n` cancels and restores the list |
| Quit | PASS: `q` closes the session, ssh exit 0 (~0.3 s) when the terminal is drained continuously (see caveat below) |
| SSH policy probes | PASS: `tests/ssh-settings-policy.py` full suite — exec/subsystem/env/signal/forwarding/X11/agent denials, one session per transport (code 4), resize + `--resize-exit-stress`, small-terminal rendering, `q` exit 0, settings pod removed on full disconnect, new settings pod runs the new image digest |
| Lifecycle probes | PASS: `tests/ssh-settings-lifecycle.py` — SIGKILLed client disconnect and actual OpenSSH control-master lifetime (pod retained after `q`, second session denied, cleanup on master exit) |
| Template rejection | PASS: `tests/ssh-template-rejection.py` — unknown template denied in 0.18 s (exit 255); `ubuntu` positive control OK |
| Ordinary persistent regression | PASS: `box-8bbfc143fa` UID `b2abba32-495f-4212-a16c-b2490144789c` Running and unchanged across every stage of this rollout (observed before, during and after) |
| Egress exception (real use) | PASS: settings pods reached the Kubernetes API through the new `containerssh-sessions-settings-api` policy — the UI showed Live and refreshed against real pod data (the only new egress) |
| **Delete + recreate from the UI** | PASS (core loop, revision 43 stage): test template `smoke-mgmt` → box `box-b98d38e9d4` created via `ssh smoke-mgmt@…` (UID `4093ac33-8e38-489a-9d43-7d37b4293476`, exactly the deterministic name `box-` + 10 hex of SHA-256 over `matthias.matt\0smoke-mgmt`); deleted **from the settings UI** (`d`+`y`): Terminating observed, row reached `not created` showing `ssh smoke-mgmt@<host>`; recreated via `ssh smoke-mgmt@…`: same deterministic name, **new UID** `d6d56f1f-11a4-4892-ad5e-6ee9eef0e002`; owner's real box untouched throughout; a driver bug that would have targeted the real box (`j` selects the sorted-second row, and `smoke-mgmt` sorts before `ubuntu`) was caught by the dialog's target-name assertion and never confirmed |
| Owner authorization | PASS: `tests/settings-owner-authorization.py` impersonating `dev-box-owner-41670cbadebcf6cd4a4d72acdc449080d4a4aef10685fa0f` — get/delete on `box-8bbfc143fa` yes; patch/update/exec no; foreign box and settings pod denied; list/watch/create/deletecollection pods no; secrets/serviceaccounts/roles/rolebindings all no |
| Cleanup | PASS: recreated test box explicitly deleted (UID `d6d56f1f…` precondition), test template removed (revision 44); final UI shows only `ubuntu`, Live, Running; session namespace holds only `box-8bbfc143fa`; no settings pods remain; gateway/config-server/auth-server logs show no unexpected errors (forwarding-rejection warnings are the probe's own denial requests) |

## Caveats and limitations

- **Un-drained PTY stalls session close after `q`.** With output continuously read (any real
  terminal, paramiko's transport thread, or a draining driver), `q` closes the session in ~0.3 s
  with ssh exit 0. A pty that never reads hangs the close — reproduced 3/3 — matching the #9
  validation's harness caveat ("did not model a user's persistent controlling-terminal session").
  No gateway or application change needed; drivers must drain.
- **OpenSSH `-tt` with piped stdout swallows exec output** on ordinary boxes (`ssh -tt … "cmd" |
  grep` prints nothing) while interactive use and paramiko both deliver output. Client-side
  artifact, not exercised by the settings UI; noted for future probes. Interactive PTY sessions
  on ordinary boxes verified working (shell prompt, command echo, output, exit).
- The not-created hint shows `ssh <template>@<host>` with a `<host>` placeholder; the TUI has no
  trusted knowledge of the public SSH hostname (unchanged from the plan).
- Push-triggered image workflows did not fire for `901d8a7` (cause not investigated); images were
  published by `workflow_dispatch` instead. Recorded here because the release pins are digests,
  so this does not affect reproducibility.
- Two-real-owner identity testing (#10) remains open and gates pre-alpha enrollment; delete is
  still destructive (no PVC, #12) and resource usage is not shown (#13).
- Calico-tier inheritance caveats from the #11 validation are unchanged and were not retested.
