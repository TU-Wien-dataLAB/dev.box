# How to accept the shell-free Settings POC (issue #9)

Scope: [issue #9](https://github.com/TU-Wien-dataLAB/dev.box/issues/9), not container
management or completion of parent #7. Use the existing development release only.
**Status: revision 21 uses the user-approved fork PR #11 image (2026-10-14),
including the resize-shutdown fix and four required Settings fixes. See
[the current live evidence](settings-fork-validation.md) for exact coverage,
retained old pods, and remaining gates.** Record each gate as PASS, FAIL, or NOT RUN with
evidence. A screenshot or a request timeout is not a security-policy pass.

## 1. Validate locally, then pause

Run from the repository root:

```bash
(cd config-server && go build ./... && go vet ./... && go test -race ./...)
(cd auth-server && go build ./... && go vet ./... && go test -race ./...)
(cd settings-tui && go build ./... && go vet ./... && go test -race ./...)
tests/chart-settings-rendering.sh
docker build --platform linux/amd64 -t dev-box-settings:local settings-tui
python3 tests/settings-image-smoke.py --image dev-box-settings:local
docker build --platform linux/amd64 -t containerssh-settings:dev containerssh-settings
CONTAINERSSH_IMAGE=containerssh-settings:dev tests/chart-config-binary-validation.sh
docker build --platform linux/amd64 -t config-server:settings config-server
```

Use the example item at
[`charts/containerssh/examples/settings-template.yaml`](../charts/containerssh/examples/settings-template.yaml),
not a replacement list of ordinary templates. **The patched gateway is required**:
stock v0.6 accepts repeated starts and ignores the configured signal denial.
The chart defaults deliberately remain on the stock gateway; registration alone
is not deployment readiness. The settings image contains no SSH server or shell.
CI in `.github/workflows/settings-images.yml` publishes the two new images with
full-commit `sha-…` tags; the existing config-server workflow publishes its rebuilt
image separately. Pin all three rollout images to their immutable tags/digests.

Collect local evidence before rollout:

- Go build/vet/tests and Docker build/smokes: packaged
  `/usr/local/bin/dev-box-settings`, compatible `containerssh-agent`, non-root
  PTY startup, read-only screen, resize, `q`, non-PTY explanation, missing-owner
  failure, and normal/error/crash termination with no shell fallback.
- Exact `settings` template and existing authorization allowlist; no new POC
  group/allowlist. Ordinary templates and unknown-name denial remain intact.
- Trusted, nonempty `DEV_BOX_AUTHENTICATED_OWNER` injected from verified auth
  metadata, never client env/login name/original command. Test concurrent
  request isolation and identities containing terminal control characters.
- Helm and webhook tests plus effective configurations through the **actual
  ContainerSSH merge** and deployed-version binary validation. Verify settings
  connection mode, direct absolute forced executable (no shell wrapper), PTY
  and application shell path allowed, exec/subsystem/env/forwarding disabled,
  and a lifetime limit of one session channel. Check explicit nonempty denial
  policies survive merging and persistent `createMissingPods` does not leak into
  settings. `--dump-config` checks parsing/defaults, not factory validation; the
  test-only Go adapter imports the real merger and calls v0.6 pod/security validators.
- Ordinary mode/name/owner/cap/reconnect behavior unchanged; ephemeral settings
  pods excluded from the persistent-box allowance.

Prepare the optional probe dependency without changing project dependencies:

```bash
python3 -m venv /tmp/dev-box-settings-probe-venv
/tmp/dev-box-settings-probe-venv/bin/pip install 'paramiko==3.5.1'
python3 -m py_compile tests/ssh-settings-policy.py
python3 tests/ssh-settings-policy.py --help
```

The exact Paramiko version is intentional: the probe uses a small private
transport seam to observe reply packets, not fire-and-forget API return values.
Review that seam before changing the version. `--help` does not import Paramiko
or connect; `py_compile` only checks syntax.

**STOP: obtain explicit approval for a live upgrade and live acceptance before
any Helm upgrade, port-forward, SSH probe, or cluster observation.** Obtain
separate approval for each disruptive fixture/image change below. Do not treat
this document, issue creation, or successful local tests as approval. Do not
uninstall the release, change host keys/Secret references, or delete an ordinary
persistent box to make acceptance easier.

## 2. Prepare approved live acceptance

1. Record approval, operator, time, release revision, gateway/auth/config/settings
   immutable image identifiers, ContainerSSH version, timeout/cleanup bounds, and
   redacted effective configuration. Preserve existing values, host-key and
   authentik-token Secret references, templates, session namespace, runtime and
   network isolation. Warn that restarting the gateway can interrupt SSH clients.
2. Record the existing `ubuntu` pod name and UID **before** rollout. Verify it is
   still the same UID afterwards. Use explicit `helm --kube-context container-ssh
   --namespace containerssh` and `kubectl --context container-ssh --namespace …`
   for every operation; never rely on default context. Have the rollout owner
   execute the separately approved upgrade/restart and readiness gates. Append
   the example `settings` item to a copy of the existing `kubernetes.podTemplates`
   list, replacing only its guest image with the immutable settings image. Select
   the patched gateway via `image.repository/tag` and the rebuilt config webhook
   via `configServer.image.repository/tag`. Do not replace the list with the
   example alone: Helm lists are replaced, not appended. Before upgrading, render
   the full intended values locally and compare the unrelated values/Secret
   references. The chart refuses settings with inherited base volumes, helper
   containers, token automount, host namespaces, management service account, fixed pod name, or
   owner label: empty/false webhook values cannot reliably clear them via mergo.
   Resolve such conflicts explicitly, not by disabling the guard. External base
   ConfigMaps/config webhooks require the same effective-policy inspection.
3. Use an existing enrolled key and the canonical authentik owner; do not enroll
   keys, modify production authentik, or print tokens/Secret contents as part of
   this probe. Keep host identity independently verified. The probe requires an
   entry for `HOST` at port 22 or `[HOST]:PORT` otherwise in `--known-hosts`.
   Do not use auto-add trust or treat unauthenticated `ssh-keyscan` output alone
   as verification. For a local forward, provision the verified `[localhost]:2222`
   entry from the established gateway host identity.
4. After approval, if needed:
   `kubectl --context container-ssh --namespace containerssh port-forward svc/containerssh 2222:2222`.
   Select a quiet interval: the lifecycle observer attributes new pods by
   settings-only selector and requires exactly one fresh pod in its happy-path
   observation. Confirm that selector against effective template labels; do not
   guess an app/owner label or include ordinary pods.

## 3. Run the thin SSH probe

After the pause is explicitly lifted:

```bash
/tmp/dev-box-settings-probe-venv/bin/python tests/ssh-settings-policy.py \
  --host localhost --port 2222 --key ~/.ssh/slurm_tu_wien \
  --owner CANONICAL_AUTHENTIK_OWNER --known-hosts ~/.ssh/known_hosts
```

Optionally add both `--second-key PATH --second-owner OTHER_CANONICAL_OWNER` for
simultaneous distinct-owner screens and second-owner reconnect. Add
`--ask-key-passphrase` for encrypted keys (one local prompt, shared passphrase
for both keys). Key search and SSH agent authentication are disabled.

For read-only Kubernetes observations, add **all** of:

```text
--kube-context container-ssh
--session-namespace containerssh-sessions
--ordinary-pod EXISTING_UBUNTU_POD_NAME
--settings-selector VERIFIED_SETTINGS_ONLY_LABEL_SELECTOR
```

For a separately approved resize/exit regression, add `--resize-exit-stress`.
It sends a bounded burst of no-reply window changes around `q`, then requires
exit 0, a healthy parent replying to the session-limit probe, fresh reconnects,
and (with observations) pod cleanup. These no-reply packets are lifecycle stress,
**not policy-denial evidence**. Check gateway restart counts before/after as well;
this option can crash gateways without the resize-queue shutdown fix.

The observer only runs `kubectl get`; it prints selected pod names, UIDs, images
and imageIDs, not full specs/env/credentials. It requires the existing ordinary
pod and preserves its UID; it never deletes/recreates it. It observes two
sequential fresh settings pods, pod presence after `q` with the transport held
open, removal after full disconnect, and no new settings UIDs remaining at the
end. It does not inspect privileges or prove image-upgrade behavior.

Set `--timeout` to the approved startup/cleanup bound (default 60 seconds,
maximum 300); `--max-total-seconds` bounds the entire run (default 1200, maximum
7200, enforced with a macOS/Linux process alarm). Operations/output are bounded; each output collection is capped at
256 KiB. Expect several dozen separately authenticated settings connections
and disposable pods. Stop on failure and preserve diagnostics. Even after a
settings failure, the probe attempts the ordinary reconnect control if the
endpoint and remaining time permit it; it does not perform remedial changes.

### Interpret the probe evidence

- `ubuntu` exec must work before and after settings probes. With observations,
  its pre-existing UID must be unchanged. Without them, persistent UID behavior
  is **NOT RUN**, not inferred from successful exec.
- PTY + shell must acknowledge success, then show Settings, POC/read-only,
  labeled exact canonical owner and `q`. A separate 40×12 startup must render
  usable text; resize must acknowledge success and leave the TUI active and
  quittable. Unchanged static lines need not be redrawn. No pixel-perfect ANSI
  styling is asserted.
- Owner/original-command/PATH/ENV/BASH_ENV spoof requests before startup must
  receive SSH failure; screen identity must still be server-derived. Active
  owner spoof, repeated shell startup, X11 and agent requests also require
  explicit failure. All channel/global request packets set `want_reply=true`.
- Exec (empty, explicit shell, harmless marker, with/without PTY), SFTP/arbitrary
  subsystems, and SCP source/sink/legacy-style exec texts require failure, not
  redirection into the TUI. These are raw SSH requests, not invocations of local
  SFTP/SCP programs; modern SCP's SFTP path is covered by the SFTP subsystem.
- Direct TCP and SOCKS' underlying `direct-tcpip`, direct Unix socket, and
  second session channels require explicit `CHANNEL_OPEN_FAILURE`. Forwarding
  probes accept only codes 1 (administratively prohibited) or 3 (unsupported);
  session-limit probes require code 4 (ContainerSSH's `ResourceShortage`).
  Connection-refused/destination-failure code 2 does **not** prove policy.
  Reverse TCP/Unix sockets require a global-request failure packet. No local
  SOCKS listener is needed: SSH-level dynamic forwarding is direct TCP.
- A second session must fail both while the TUI is active and after `q` on the
  **same authenticated transport**. Closing that transport instead of replying
  is inconclusive and fails the probe. Non-PTY shell startup must be followed
  by a terminal/PTY explanation and nonzero application exit within the bound.
- Typed shell/command text, escape, Ctrl-C and Ctrl-Z are exercised on separate
  connections. A split harmless marker distinguishes execution from input
  echo. Observe no execution marker, then `q`/application exit. This is bounded
  negative evidence, **not** proof against every escape sequence or subprocess;
  retain app review, image checks and the manual gate below.
- Timeout, disconnect without the expected reply, generic SSH error, destination
  failure, or fire-and-forget API success never counts as a denial. A negative
  request accepted by SSH is a FAIL even if it produces no output or the
  destination is unreachable. Abandon the connection immediately on failure;
  inspect possible listeners/sockets/side effects rather than declaring safety.

Exit 0 means only the selected automated checks completed. Without the second
key or kubectl options, the script prints the corresponding NOT RUN notices.
It does not mark issue #9 accepted or automatically retry failed security checks.

## 4. Complete the remaining staged gates

Run these only after approval, with before/during/after evidence and bounded
waits matching configured backend cleanup. Stop at each failed gate.

| Gate | Procedure and required evidence |
| --- | --- |
| Auth/authz ordering | With approved fixtures, test unknown/rejected and ambiguous keys, existing group denial when configured, and unknown template names. Require prompt auth denial and no settings pod/TUI. Confirm a working enrolled-key positive control first. Do not create ambiguity in production user records just for testing; use approved isolated fixtures. Record group gate as NOT CONFIGURED when absent, not PASS. |
| Missing trusted identity | Use an approved isolated config-webhook fixture that omits/empties verified metadata. Require fail-closed denial/no invented `settings` owner. A denied client env spoof is not this test. Restore configuration and repeat the positive control. |
| Terminal/input | Use real `ssh -tt` with strict host verification and enrolled key. Check resize/small terminal, typed commands/escape/suspend, no remote shell prompt, `q`, and local terminal restoration. Paramiko owns no local terminal and cannot prove restoration. Observe application/process logs or approved isolated execution instrumentation for absence of subprocess/file side effects; do not write into ordinary boxes. |
| Startup/error/crash | Use approved pinned test images/fixtures for missing executable/startup error and unexpected TUI termination. Require bounded application/session failure, no shell fallback, and cleanup after full disconnect. Record exit statuses and pod UIDs. Never terminate ordinary pods or assume a non-PTY failure proves crash safety. |
| Abrupt disconnect | Start a settings TUI, record its fresh pod UID, then terminate only the test client abruptly. Observe removal within configured cleanup bounds while Kubernetes API is healthy; ordinary UID unchanged. |
| Multiplexed lifetime | Keep the SSH parent/control master alive after `q`; record channel exit, second-session denial, and settings pod still present. Terminate the parent and observe removal. Do not claim pod deletion on `q` alone. The automated happy path holds a Paramiko parent transport for this distinction; additionally exercise the actual OpenSSH control-master workflow. |
| Image upgrade | Pause for a separate upgrade approval. Hold an old settings connection open, record UID/imageID, switch to a different immutable settings image via existing release, then connect anew. Require a different UID and new configured image/digest, not a reused persistent settings box. Old sessions need not migrate; a gateway restart may interrupt them. End test parents and verify cleanup. Preserve ordinary UID and all unrelated values/Secrets. |
| Effective guest inspection | With explicit context/namespace, inspect the actual settings pod while its TUI is live: agent-compatible image/command, trusted owner env source, connection-mode unique name, non-root UID, resource limits, no privilege escalation, expected RuntimeClass/network isolation, no service-account token (including projected volumes), authentik/host credentials, hostPath/privileged mounts, persistent user PVCs, or management RBAC. Inspect gateway/config-webhook effective policy as well as the pod: a pod spec cannot prove SSH denial policy. Redact credentials; never publish full Secret data. |
| Allowance regression | With approved isolated owner/cap fixtures, retain existing ordinary boxes at the cap, open multiple settings connections and verify they do not count as persistent boxes. Ordinary reconnect still works with the same UID/name/owner labels; genuinely new ordinary boxes remain capped. Never delete a real user's box to reach a boundary. |
| Final recovery/cleanup | Disconnect only test clients/masters, restore all temporary policy/images/fixtures via separately approved changes, wait for only settings test UIDs to disappear, and rerun ordinary exec/reconnect with the original UID. Any stale test socket/listener/pod cleanup requires explicit identification and approval; do not use broad deletion selectors or uninstall the release. |

## 5. Report without overstating coverage

Attach invocation (redacted paths/owners as appropriate), dependency version,
release/images/configuration identifiers, timing/bounds, actual request reply
outcomes, owner-isolation result, settings pod UIDs/images/cleanup, and ordinary
UID before/after. Exclude private keys, public-key contents, tokens and Secret
contents. Mark omitted/disruptive gates **NOT RUN** and explain why. If a run
fails partway, later checks are NOT RUN even if an earlier screen rendered.
Pause for review of evidence before declaring #9 accepted; this POC grants no
management functionality and makes no claim about parent #7 readiness.
