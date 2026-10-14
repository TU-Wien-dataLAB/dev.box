# Settings POC — fork image validation (revision 21)

Status: **deployed for testing with user approval**, 2026-10-14. Selected live
policy/lifecycle and resize-shutdown checks passed; this is not full issue #9
acceptance. This report supersedes the revision-20 gateway selection in
[the initial live evidence](settings-live-validation.md).

## Release and image

| Field | Observed value |
| --- | --- |
| Context / release / namespace | `container-ssh` / `containerssh` / `containerssh` |
| Helm revision / chart | **21** / `containerssh-0.3.2` |
| Fork PR / target | [TU-Wien-dataLAB/ContainerSSH #11](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/11) / `deploy`; still open, not merged |
| Source head | `7f8f7a2ce0035f910815f0989c1f87910d1d799d` |
| CI source merge revision | `c9ef22d3d6ad48aa3125cbcde780e75921742e92` |
| Gateway reference | `ghcr.io/tu-wien-datalab/containerssh:pr-11@sha256:529a4f5618be89a56e3714fe9f77cf886a6674131e0cec5f5f1b28841d92beb9` |
| amd64 manifest digest | `sha256:aecd6105063f5fee54d6654441efa2531b5890ac5e8ee9fa50b45e96dcf0ea86` |
| Gateway pod / UID | `containerssh-7cb46688ff-z9r2m` / `ff3dd397-2dc2-434b-acb1-1b85fd676052` |
| Gateway restarts after checks | **0**; same gateway UID throughout |

Anonymous pull, published source labels, amd64/arm64 platforms, and deployed
image/imageID were verified. The reference is pinned by digest despite the
mutable `pr-11` tag. [Image publication](https://github.com/TU-Wien-dataLAB/ContainerSSH/actions/runs/38551719210)
succeeded after fixing same-repository PR publishing in the fork.

The upgrade changed only gateway image values. Rendered objects were identical
except for the gateway image and the associated **unexecuted** Helm test-hook
image. Auth/config/guest images, Secret references, templates, runtime/network
isolation, Services, and all other user values were preserved. All three
Deployments are Ready, and applied values match the reviewed rollout.

## Fixes retained in the fork image

The PR now contains all five required fixes, not only resize shutdown:

1. One successful shell/exec/subsystem start per channel; failed starts retryable.
2. Signal policy reads `Signal.Mode`, independently of `Shell.Mode`.
3. Connection-pod readiness failures trigger independently bounded rollback;
   persistent/session creation lifecycle is unchanged.
4. Four-byte PID framing for connection/persistent exec before initial resize;
   session-mode PTYs retain their six-byte wait/echo framing.
5. Resize shutdown signals a separate channel instead of closing the data channel;
   blocked operations wake, late pushes reject, and repeated Stop is safe.

Each of the four added regressions failed before porting its production fix.
All five regressions passed 20 race-enabled repetitions; resize alone also
passed 50 repetitions. Native build, targeted vet, local/CI lint, actual fork
merge and security/pod validation, and published-image parsing/default checks
passed for the real base and effective Settings/ordinary configurations.

The full CI check is **not green**: `TestSmallUpload`, `TestLargeUpload`, and
`TestS3` cannot pull `docker.io/minio/minio`, identically to the `deploy` baseline.
Latest CI confirmed all five gateway regressions pass; unrelated S3 failures
were not changed. Audit logging is disabled in this deployed configuration.

## Live results

Probe: Paramiko 3.5.1, existing enrolled key and independently trusted host key,
90-second per-operation / 1,800-second total bounds, explicit context/namespace,
and Settings-only selector `dev.box/application=settings`.

| Check | Result |
| --- | --- |
| Settings PTY/owner/quit | PASS: Settings, POC/read-only, canonical owner, quit hint; `q` exits 0 |
| All SSH policy denials | PASS: exec/SFTP/SCP/subsystem, spoofed env, signals, repeated starts, forwarding/X11/agent; explicit reply/failure codes verified |
| Session lifetime | PASS: second session code 4 while active and after `q` on the same healthy transport |
| Size/input/non-PTY | PASS: 40×12 startup, resize, inert command/ESC/Ctrl-C/Ctrl-Z input; non-PTY explanation and exit 1 |
| Resize/exit regression | PASS: **4 fresh connections × 28 no-reply window changes = 112 resize packets** around `q`; exit 0 and healthy parent response each time |
| Pod cleanup/reconnect | PASS: each observed new Settings UID disappeared after full disconnect; next connection used a fresh UID |
| Real OpenSSH | PASS: PTY startup, resize/inert input, quit, operational terminal restoration, and test-pod removal |
| Effective guest | PASS for inspected isolation fields; same inherited Calico passthrough caveat as initial evidence |
| Unknown template | PASS: prompt `Permission denied`, 0.41 s / exit 255; ordinary control succeeds |
| Gateway/forward stability | PASS: same gateway UID, zero restarts; local forward still listening after repeated Settings sessions |
| Ordinary box | PASS: `box-8bbfc143fa`, UID `b2abba32-495f-4212-a16c-b2490144789c`, retained Running and exec/reconnect succeeded |

The no-reply resize packets are stability stress, not policy-denial evidence.
`q` ends the application/session; the connection-mode pod remains until the
parent SSH transport fully disconnects. Connection sharing can retain it.

Stress-test pod UIDs, all removed afterwards:

- `settings-tvvjk`: `8475216c-a494-4df6-8f9a-b2d4587355ae`.
- `settings-k74mm`: `fb26ecb9-2905-4a96-80cc-cc44420fc929`.
- `settings-pptfh`: `3783acd1-c6f7-474d-bdba-6d91a29d86f3`.
- `settings-dhkf8`: `2085b2ae-149f-4f69-8597-f0feccb7a228`.

Final OpenSSH inspection: `settings-4gxc7`, UID
`b24b765e-b576-4d18-94ca-470e306ee549`, removed afterwards. The first OpenSSH
harness attempt was inconclusive because it counted the old stranded pods;
filtering against the pre-test UID baseline corrected the harness, not the endpoint.

## Retained old pods and remaining gates

These revision-20 crash leftovers were **not deleted**; names/UIDs remain unchanged:

- `settings-jdk6x`: `52b63021-19d7-4977-8176-bf30856c524c`.
- `settings-xrtw4`: `3c3aa811-a5b7-43cd-b33e-9b34fb4bea33`.

Before/after namespace pod maps are identical: the ordinary box and these two
old pods remain, with no new test Settings pods. Removing old leftovers requires
separate explicit, UID-preconditioned cleanup approval. The fix prevents this
resize panic; it does not add orphan garbage collection after arbitrary gateway loss.

Distinct-owner isolation, live missing-identity/ambiguity/group fixtures, deliberate
crash/alternate-image/cap fixtures, actual OpenSSH control-master workflow, human
visual inspection, and end-to-end Calico authorization remain outside this run.
See [the acceptance runbook](settings-acceptance.md); issue #9 stays open.

Restricted local evidence: `/tmp/dev-box-settings-retest.cz6NVA/`, including
`ssh-policy-resize.log`, `openssh-smoke-final.log`, gateway/user snapshots,
published-image metadata, reviewed/applied values, and parser/merge outputs.
Private keys, tokens, and Secret contents are not included in this report.
