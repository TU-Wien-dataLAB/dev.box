# Settings POC — additional acceptance evidence

**Historical pre-fixture issue #9 evidence.** Later image/failure/quota and missing-owner
SSH checks, plus restored revision 38, are in [fixture evidence](settings-fixture-validation.md).
Neither report validates issue #11's synthesized settings/scoped-token contracts; follow
[the new migration and staged plan](settings-code-migration.md). Recorded results below are unchanged.

Date: **2026-10-16**. Release remains revision **21**, with the digest-pinned fork
image in [revision-21 evidence](settings-fork-validation.md). Issue #9 remains
open. No release values, auth records, or existing pods were modified.

## Newly completed live checks

| Gate / seam | Result |
| --- | --- |
| Abrupt client disconnect, real SSH → Kubernetes | **PASS**: an active Settings OpenSSH process was terminated with local SIGKILL; only its fresh Settings pod disappeared within the 90-second bound |
| Actual OpenSSH control-master lifetime | **PASS**: `q` exited the application client with 0; the master remained healthy, retained its pod, and explicitly refused another session; acknowledged master shutdown removed that pod |
| Unknown key, real SSH → authentik | **PASS**: a generated, unregistered, in-memory key received an SSH authentication rejection in **0.353 s**, without changing the session-pod UID baseline |
| Missing identity, deployed config webhook | **PASS at webhook seam**: omitted, null, empty, and whitespace authenticated-owner fields each returned HTTP 500; positive requests before/after returned 200 |
| Concurrent identities, deployed config webhook | **PASS at webhook seam**: 64 Alice/Bob fixture requests across eight workers each received exactly one expected owner variable; no cross-owner mutation |
| Quota labeling, deployed config webhook | **PASS at webhook seam**: Settings responses retained connection mode, no fixed persistent name/creation flag, and no `dev.box/owner` label |
| Supporting auth/config contracts | **PASS**: full suites each passed ten race-enabled repetitions, including missing-owner, concurrent-owner, ambiguous lookup and quota-boundary contracts |
| Ordinary control / final baseline | **PASS**: ordinary SSH exec succeeded before/after; `box-8bbfc143fa` retained UID `b2abba32-495f-4212-a16c-b2490144789c`; no new Settings pods remain |
| Gateway/release preservation | **PASS**: revision/values unchanged, every management-pod UID/restart count unchanged; gateway retains UID `ff3dd397-2dc2-434b-acb1-1b85fd676052` and **zero restarts** |

The config-webhook requests use **synthetic authenticated metadata** sent through
an operator-owned local port-forward. They do not authenticate those fixture
owners through authentik and do not prove the complete missing-identity SSH path
or two real owners' screens. The webhook denies valid missing-owner requests with
a generic error response; internal error details are intentionally not exposed.

Final successful lifecycle run:

- Abrupt disconnect: `settings-5vvd6`, UID
  `b160c023-fd68-45b7-af8a-da4c84763b78`, removed.
- Control master: `settings-wvmsg`, UID
  `7f4cc242-18ab-403d-b118-7ac428a0d617`, removed.

The final probe checks the ordinary pod before any SSH connection, rejects empty
contexts before running subprocesses, and verifies cleanup by recorded pod name
and UID (not just disappearance from a label selector). Isolated fake-CLI
preflight checks confirmed missing ordinary pods never trigger SSH recreation.

Earlier harness runs were inconclusive, not product failures: the initial quit
wait did not drain the PTY; OpenSSH also falls back to a **new TCP connection**
after mux-session refusal unless that fallback is disabled. The final probe drains
output and forces the fallback to fail locally. An acknowledged `-O exit` can
leave the `-N` master with local status 255 because it has no remote command exit
status; the probe verifies master unreachability and pod removal rather than
requiring an invented remote-program status. Earlier test pods also disappeared.

The two old revision-20 crash leftovers were already absent at the initial
2026-10-16 snapshot. This run issued no Kubernetes pod deletions and makes no
claim about who previously removed them. The initial and final session namespace
contain only the same ordinary box.

## Still needed before complete acceptance

| Gate | Requirement |
| --- | --- |
| Two real owners, concurrent screens/reconnect | A second existing enrolled identity and operator-authorized test access; no key enrollment or production authentik modification performed |
| Settings guest image upgrade | Separate approval for temporary, digest-pinned Settings-image change and restoration; a gateway-image upgrade is not this test |
| Missing executable/startup failure and unexpected TUI termination | Separately approved isolated failure images/fixtures; non-PTY failure and killed client are not substitutes |
| Missing-owner complete SSH path | Separately approved isolated metadata fixture; direct webhook denial above is supporting evidence only |
| Persistent allowance at its boundary | Approved isolated owner/cap fixtures; no real box deletion, release-wide cap modification, or artificial owner-label pods performed |
| Ambiguous-key auth / configured group denial | Isolated auth fixtures; production key ownership was not duplicated; group gate is not configured |
| Instrumented absence of side effects / Calico authorization | Remains outside this run; existing bounded marker and core management-access checks are not blanket proofs |

Reproduction procedure: [acceptance runbook](settings-acceptance.md). Restricted
local snapshots/logs: `/tmp/dev-box-settings-remaining.5uP6cf/`, including
`ssh-lifecycle-final.log`, `live-config-contract.json`, `unknown-key.json`,
before/after UID snapshots and values, and race-test logs. No private-key material,
tokens, or Secret contents are published in this evidence.
