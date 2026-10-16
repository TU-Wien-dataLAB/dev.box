# Settings POC — approved live fixture validation

> **Historical issue #9 / revision-38 evidence.** The values-defined settings item, inherited Kata
> and no-token floor below precede issue #11. PR status is as recorded at the time: fork PR #11
> subsequently merged on 2026-10-16; deploy-line Docker Image
> [run 38917607974](https://github.com/TU-Wien-dataLAB/ContainerSSH/actions/runs/38917607974) succeeded.
> The merge did not change revision 38's pinned image. Follow
> [the new migration and staged plan](settings-code-migration.md) for the new contracts;
> historical evidence below is unchanged.

Date: **2026-10-16**. User approved temporary Settings-image/failure/quota
fixtures and ContainerSSH-related restarts. Final release: **revision 38**, chart
`containerssh-0.3.2`, context `container-ssh`, namespace `containerssh`.

**The approved fixture gates passed.** Original user values were restored exactly.
Issue #9 remains open: no second real enrolled identity was available, and the
additional limitations below are not waived. Earlier evidence:
[revision 21](settings-fork-validation.md), [non-disruptive checks](settings-remaining-validation.md).

## Release and fixture artifacts

| Field | Observed value |
| --- | --- |
| Final gateway image | `ghcr.io/tu-wien-datalab/containerssh:pr-11@sha256:529a4f5618be89a56e3714fe9f77cf886a6674131e0cec5f5f1b28841d92beb9` |
| Final gateway pod / UID | `containerssh-7cb46688ff-6ctbm` / `e149c965-ca3f-4d9c-af7f-8c08ce3a16c4` |
| Final gateway container restarts | **0**; planned gateway rollouts occurred for metadata-fixture routing/restoration |
| Alternate guest image | `ghcr.io/tu-wien-datalab/dev.box/settings-acceptance:sha-33bdb0fa99fe33429b690520b7409c3fbdb059bb@sha256:7d138981d7447a2e26cd1311dd25aae996f1c9881fb3d5dee96438453bddb32d` |
| Alternate amd64 manifest | `sha256:cabe25dce3cbd0464df6640656a730c0295df67dbf0c8ce1367d98d3c74ab28c` |
| Fixture source / publication | `33bdb0fa99fe33429b690520b7409c3fbdb059bb`; [successful manual CI build](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/38896698821) |
| Ordinary box | `box-8bbfc143fa`, UID `b2abba32-495f-4212-a16c-b2490144789c`, retained Running throughout |

Anonymous registry pull, source revision, platform, non-root user, and unchanged
direct TUI entrypoint were verified. The alternate image adds only the
[acceptance helper](settings-fixture/README.md) to the pinned original guest;
it does not replace the TUI or add a shell. CI ran race tests/vet, including
real-process kill safeguards inside its disposable Linux build stage.

## Results at the complete SSH/backend seam

| Gate | Result and evidence |
| --- | --- |
| Persistent allowance boundary | **PASS**: temporary cap **1**, occupied by the existing ordinary box; **two simultaneous Settings connections** rendered the canonical owner without persistent owner labels; ordinary reconnect succeeded; a different persistent template could not execute/create a pod |
| Cap-denial cause | **PASS**: actual config-server logs recorded `persistent pod cap reached` for `issue9-cap-probe`; client channel open ended with remote EOF, not a local timeout; no new persistent pod existed |
| Settings image upgrade | **PASS**: held the old session open; new connection received a different pod UID and the alternate configured image/imageID; old connection remained usable with its original UID/image |
| Unexpected TUI termination | **PASS**: administrative helper killed exactly the same-UID TUI process; application channel closed with **137**, parent remained healthy, second session explicitly denied with code **4**, idle container had zero restarts; full disconnect removed the pod |
| Missing executable | **PASS**: direct forced executable changed to a nonexistent absolute path; application channel closed with **127**, without TUI or shell fallback; full disconnect removed the pod |
| Missing verified owner, complete SSH path | **PASS**: existing real enrolled key authenticated normally; isolated config proxy removed only that owner's `authenticatedUsername` for `settings`; real config-server denied, SSH channel opening ended with remote EOF in **61.647 s**, and no Settings pod appeared |
| Metadata-fixture control | **PASS**: ordinary SSH still worked through the proxy unchanged; restoring normal routing returned a working Settings TUI with canonical owner |
| Final policy regression | **PASS**: full existing SSH policy/lifecycle probe and four resize/exit stress runs passed again (**120 resize packets**); removed cap-probe template denied promptly in **0.48 s**, exit 255 |
| Final restoration | **PASS**: original values/Secret references/images/templates/cap restored; all three Deployments Ready; session namespace contains only the original ordinary UID; no fixture pods remain |

SSH authentication may succeed before backend configuration. Cap and missing-owner
failures were therefore tested by attempting the application/session, correlating
backend evidence, and requiring no new pod—not by treating `connect()` alone as
backend acceptance. Remote EOF is not counted as SSH policy-request denial;
explicit packet/code assertions remain in the final policy probe.

## Recorded pod identities

All listed test UIDs disappeared after their own transports disconnected:

| Purpose | Pod | UID |
| --- | --- | --- |
| At-cap Settings 1 | `settings-zjwt9` | `e75aba8f-1aa8-4786-aee4-6a7d792c02e9` |
| At-cap Settings 2 | `settings-z4wt5` | `39c70b5e-90fb-49f0-b88d-525200eeb80b` |
| Old image/session | `settings-cqc5h` | `42a94ed1-07a9-45c3-9a40-30d8fd1a6b14` |
| New image/session | `settings-5qb4j` | `dda0ecd6-cefb-4502-a7cd-332f1f037c8a` |
| Killed TUI | `settings-k9bjh` | `7f30dd7e-db6c-48ed-bd68-2d4bdf1a83fd` |
| Missing executable | `settings-q5mx8` | `d15f348a-66e3-4370-a97c-8bc8368e9dd3` |
| Post-restoration positive control | `settings-7nd46` | `48b01135-d50b-439e-a764-0614469f9fc0` |

The temporary management-namespace proxy pod and Service were explicitly removed
with UID-preconditioned DeleteOptions:

- Pod `issue9-metadata-fixture-33bdb0`: `0491fbc0-d2a7-43a8-8d4d-a62992b2a163`.
- Service of the same name: `f9dc0e5e-703a-4234-a128-549cc4915deb`.

No ordinary pod was deleted/recreated. No authentik record, token, host key,
Secret reference, RBAC rule, or network policy was changed.

## Interpretation and remaining limits

Earlier harness attempts were inconclusive and restored before retrying:
authentication alone was not a cap assertion, stale config-pod forwards failed
after rollouts, remote EOF needed explicit handling, and the fixture URL needed
`/config` because ContainerSSH posts to its configured URL unchanged. These are
not reported as application failures or extra passing cases.

Still **NOT RUN**: two real distinct enrolled owners' simultaneous screens and
reconnects; live ambiguous-key auth fixtures; instrumented absence of every
execution/file side effect; human visual acceptance; end-to-end Calico
authorization. Group gate is **NOT CONFIGURED**. Existing local concurrency,
ambiguity, missing-owner and quota tests remain supporting evidence, not
substitutes for missing live fixtures. Fork PR #11 remains unmerged.

The helper's process checks are temporary administrative instrumentation, not
a general process-isolation guarantee. PID-safe use assumes pidfd support;
Go can fall back on unsupported kernels. The runtime control reports Kata guest
kernel `Linux 6.18.35`; do not reuse this helper on arbitrary hosts or against
untrusted concurrent process changes.

Restricted evidence: `/tmp/dev-box-settings-fixtures.JvjDpe/`, including
`quota-result.json`, `quota-config-server.log`, `image-failure-results.json`,
`missing-owner-result.json`, `final-policy-probe.log`, original/final values,
UID snapshots, CI logs, and the exact fixture-driving scripts. Tokens, private
keys, and Secret contents are not included in this report.
