# Settings as code — live migration and acceptance evidence

Date: **2026-10-19**. Scope: [dev.box #11](https://github.com/TU-Wien-dataLAB/dev.box/issues/11).
The user approved “migrate and test on the live deployment”. Context `container-ssh`, release
`containerssh`, management namespace `containerssh`, session namespace `containerssh-sessions`.

**The migration and selected issue #11 live gates passed.** Release **41**, chart **0.3.3**,
is deployed; all three management Deployments are Ready with zero container restarts.
The ordinary Kata box retained its original UID. Temporary fixtures are removed. The unchanged
static TUI now runs without Kata and receives a scoped projected service-account token.
This is not acceptance of #7's management actions or #10's two-real-owner identity testing.

Procedure: [migration and staged plan](settings-code-migration.md). Historical POC reports do not
substitute for the new observations below.

## Release and image provenance

| Item | Observed value |
| --- | --- |
| Previous release | revision **40**, chart **0.3.2** |
| Final release | revision **41**, chart **0.3.3**, Helm status `deployed` |
| Implementation source | `5bca1715298e0655dce7f411062c60064da57f0c`, including #11 commit `b040bde` |
| Published config server | `ghcr.io/tu-wien-datalab/dev.box/config-server:sha-5bca171@sha256:b74681bf6d3cd12ccfd01f7e4b6f5d368386882b297375d77e030f782dc204dc` |
| Config-server publication | [successful CI run 39228451570](https://github.com/TU-Wien-dataLAB/dev.box/actions/runs/39228451570); pulled amd64 image has the exact source revision label |
| Unchanged Settings image | `ghcr.io/tu-wien-datalab/dev.box/settings-tui:sha-4285f7893a1851eecf6eeec177a9964ef6815d3a@sha256:addad96cb660725389df3cf602d5b183db6864c15e60ae1766f6d768cf614252` |
| Preserved gateway | `ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d` |
| Preserved auth server | `ghcr.io/tu-wien-datalab/dev.box/auth-server:sha-245b9e2@sha256:36e68dc2e1686378121a37aebff86ff4c28f42ffe127f9903bae2fe30c0e5f73` |
| Local reviewed chart artifact SHA-256 | `3c2ec1db627f2b0e20aaae0ca5560fe791f3af72e1e8bbe07807ed8884ebd192` |

The earlier local Docker-build disk-space blocker is historical: CI published the reviewed
config-server source, and its digest-pinned image was pulled and deployed successfully.

### Approved value changes and rollout

Exactly three user-value changes were applied: remove the old `settings` pod-template item,
copy its unchanged pinned guest image to `settings.image`, and update `configServer.image.tag`
to the published pinned image above. The ordinary `ubuntu` template, gateway/auth pins, cap,
Secret references, host key and all other user values were preserved. No base-field relocation
was necessary. Final exported user values equal the reviewed migration values exactly.

The rendered changes remove base RuntimeClass/creation flags, inject Kata into `ubuntu.yaml`,
wire the Settings image and template checksum, and expand config-server namespace RBAC to
pods `list/get/delete` and serviceaccounts/roles/rolebindings `get/list/create/update`.
There is no `bind`/`escalate` or cluster-wide config-server grant. Existing NetworkPolicy,
Services, Secrets, RuntimeClass, Kata installer and Helm hooks were compared and preserved.

The applied command, with artifacts kept outside the repository, was:

```bash
helm upgrade containerssh "$work/containerssh-0.3.3.tgz" \
  --kube-context container-ssh --namespace containerssh \
  -f "$work/reviewed-user-values.yaml" --wait --timeout 5m --rollback-on-failure
```

Original values, manifests, hooks, pod/object inventories, revision 40 and a rollback chart
artifact were secured before upgrading. No rollback was needed; fixture tests did not make
additional Helm revisions or change production templates/owner grants.

## Local preflight and effective configuration

**PASS:** build, vet and uncached race tests in `config-server`, `auth-server`, `settings-tui`,
`tests/config-merge` and `tests/settings-fixture`; all seven chart scripts, including real-binary
validation against the preserved PR #10 gateway. The config-merge module has no unit tests;
its real defaulting/merger/backend/security validators were exercised by the binary script.

**PASS:** Docker smoke against the unchanged published Settings image: shell-free non-root
entrypoint, terminal/resize/inert-input/q/restoration behavior, no-PTY and empty-owner failures,
owner display sanitization, agent console launch, and idle-agent INT/TERM behavior.

**PASS:** actual HTTP responses from the published live config server for `settings` and
`ubuntu`, decoded into the actual v0.6 `AppConfig`, serialized with its YAML marshaler, then
merged with the reviewed live base using the real merger and backend/security validators.
Both effective configs passed the pinned gateway's `--dump-config`. Settings retains connection
mode, explicit deny policy, scoped token, no persistent name/creation flag/RuntimeClass;
ordinary retains persistent creation, deterministic identity and per-template Kata.

## Live acceptance results

| Gate / seam | Result |
| --- | --- |
| Ordinary SSH before/after | **PASS:** enrolled-key exec/reconnect succeeded before rollout, throughout probes and at final control; kernel `6.18.35`, unchanged `box-8bbfc143fa` UID `b2abba32-495f-4212-a16c-b2490144789c` |
| Reserved synthesis | **PASS:** template ConfigMap contains only `ubuntu.yaml`; Settings SSH succeeds with canonical owner `matthias.matt`, static POC/read-only/q screen and no values-defined Settings file |
| SSH policy | **PASS:** existing packet-level probe verifies explicit failure for exec/SCP/subsystem/env/signal/X11/agent/TCP/Unix/reverse forwarding; repeated shell rejected, second session code **4** before/after q, forwarding channel code **1**; ordinary positive control retained |
| Terminal behavior | **PASS:** PTY/shell, small terminal, resize, inert typed commands/ESC/Ctrl-C/Ctrl-Z, helpful no-PTY exit **1**, q exit **0**; four resize/exit stress runs sent **33** packets in total with healthy parent transports |
| Template negatives | **PASS:** `unknown`, absent ordinary `default` and `issue11-no-template` each denied with `Permission denied` in **0.20–0.22 s**; empty SSH login rejected in **0.129 s** |
| Effective Settings pod | **PASS:** no RuntimeClass or persistent owner label; one unchanged guest container; UID/GID 1000, non-root, RuntimeDefault seccomp, read-only root, all capabilities dropped, no privilege escalation, expected resources, direct idle agent and canonical owner env; no host namespaces, PVCs or unrelated Secret mounts |
| Scoped account/token delivery | **PASS:** pod automount true, owner ServiceAccount default false; kubelet-injected projected token/CA/namespace volume, read-only standard mount and actual file-presence probes described below |
| Kubernetes authorization | **PASS:** existing read-only helper's **33** impersonated checks, including service-account groups, allows get/delete of the exact ordinary name and denies foreign/settings names, namespace list/watch/create/deletecollection, pod mutations/exec and SA/RBAC/Secret access. Five additional checks deny named logs, exec, cross-namespace pod access, named Secret and token issuance |
| Parent transport lifetime | **PASS:** q ends the app while its parent transport/pod remains; full disconnect removes that pod; reconnect creates a fresh UID |
| Abrupt disconnect / OpenSSH master | **PASS:** actual OpenSSH SIGKILL cleanup; actual control master keeps the pod after q, explicitly rejects a second session without fallback, remains healthy, then removes the pod after acknowledged master shutdown |
| Concurrent real sessions | **PASS:** two simultaneous enrolled-key sessions for the same real owner use distinct disposable pod UIDs and the same owner account; no per-connection authorization objects |

Timeout, EOF, authentication alone and destination failures were not counted as SSH policy denials.
The real SSH probes were `tests/ssh-settings-policy.py` (including resize stress),
`tests/ssh-settings-lifecycle.py` and `tests/ssh-template-rejection.py`, via an owned loopback
port-forward on port 32222. Host verification used the previously trusted `[localhost]:2222`
public host key; strict host verification remained enabled.

### Recorded production objects and mounted-file observation

All three owner objects are named
`dev-box-owner-41670cbadebcf6cd4a4d72acdc449080d4a4aef10685fa0f`:

| Object | UID |
| --- | --- |
| ServiceAccount | `4aa9a2e8-11cf-4cee-a114-70d114c57d6d` |
| Role | `e700691e-cf27-4274-a3a2-921ad61cc9cc` |
| RoleBinding | `997bd773-b206-4f02-93b0-5b74f004fedc` |

The Role has one core `pods` rule: verbs `[get, delete]`, resourceNames `[box-8bbfc143fa]`.
The binding points only to that Role and that session-namespace account. These UIDs and rules
remained stable across reconnects and concurrent real Settings sessions.

Inspected disposable pod `settings-x7xf8`, UID `9b86b51d-a1af-427a-b4da-2f7a71469deb`, used
that account, no RuntimeClass, and the unchanged guest digest. Its projected volume mode is
0644. Operator exec of the existing agent's `console` mode attempted `execve` of each standard
`token`, `ca.crt` and `namespace` path: each returned **EACCES**, versus **ENOENT** for a deliberately
absent sibling. These non-content path probes establish presence without reading credential
bytes or executing a file; no shell, ephemeral container or guest-image modification was added.
After q/full disconnect its exact UID disappeared.

### Isolated reconciliation and fail-closed fixtures

**PASS at the config-webhook + actual Kubernetes API seam**, using the same published
config-server image in a temporary management-namespace Deployment. It had a dedicated temporary
ServiceAccount and session-namespace Role/Binding, separate ConfigMaps, and synthetic verified-owner
metadata. Production templates and the real owner's authorization objects were not modified.
These are not real authentik-owner SSH isolation tests.

- Concurrent synthetic owners receive their own deterministic accounts; each owner's three object
  UIDs stay stable. A malformed reserved `settings.yaml` fixture is ignored, while ordinary
  malformed files deny. Connection-mode templates do not enter persistent name grants.
- Adding/removing ordinary persistent templates updates the same Role; a removed name loses
  get/delete authorization **while its disposable fixture pod still exists**.
- Zero eligible templates produce zero rules and denied get/delete, not an all-name grant.
- Empty/whitespace identities, malformed ordinary templates, a missing template directory and an
  unreachable Kubernetes API each return HTTP **500**, without a successful config.
- Missing create permission for each of serviceaccounts/roles/rolebindings denies. Drift forcing
  each object's update path also denies when its update permission is removed; restoration and
  another request reconcile successfully.
- Removing held pod-delete permission makes the real API reject role delegation: no
  `bind`/`escalate` shortcut. Restoring it returns a healthy Settings response.
- With cap **1** occupied by disposable fake-owner pod `box-d1cb51bbdf`, an ordinary reconnect
  response succeeds, a second persistent name denies, and two Settings responses succeed without
  persistent labels/creation flags. Logs explicitly record `persistent pod cap reached`.
  This is the live webhook/Kubernetes cap seam, not a new complete SSH-at-cap fixture.

All fixture objects, including partial objects from denied creation paths and the disposable cap
pod, were deleted with exact UID preconditions and subsequently checked absent. No real box was
deleted. Reconciliation failures may leave old/partial grants until operator action or a later
successful reconciliation; the tests do not establish atomic revocation.

## Authorization and networking limits

- **Existing inherited Calico grant:** the owner's groups also inherit cluster binding
  `calico-tiered-policy-passthrough`. Its ClusterRole allows get/list/watch/create/update/patch/
  delete/deletecollection on `projectcalico.org` networkpolicies/globalnetworkpolicies. It does
  not grant core pod access. This pre-existing binding was inventoried, not changed; downstream
  Calico authorization was **NOT TESTED**. The pod checks prove the core-pod boundary, not blanket
  least privilege or end-to-end cluster/network-policy isolation. This inherited authority needs
  review before future token-consuming management features.
- Standard inherited discovery/self-review grants and service-account issuer discovery also
  exist. No extra core pod grant was found in the matched bindings.
- **Settings-origin API reachability:** blocked by the unchanged session NetworkPolicy; no API
  egress exception was added. No timeout was counted as authorization evidence, and no new
  Settings-origin network probe was performed.
- **Mounted-token authentication:** **NOT RUN**. File presence/projection and operator
  impersonation are distinct from validating the actual bearer with TokenReview or a
  token-authenticated request. Credential bytes were never read, logged or exported.
- Two distinct real enrolled owners remain **NOT RUN**, tracked in #10. Human visual acceptance
  and management actions are not claimed. The static TUI consumes no token and is unchanged.

Initial throwaway harness attempts with an invalid remote-address encoding and a Python
JSON-to-YAML duration conversion were inconclusive; corrected requests and actual Go
JSON/YAML encoding were rerun successfully. Neither required a production rollback or source fix.

## Final state and restricted artifacts

Only the original Running/Ready ordinary pod remains in the session namespace, with its original
UID and Kata runtime. Exactly the real owner's three intended persistent authorization objects
remain; every temporary fixture was checked absent. Final values equal the reviewed migration
values. Gateway, config-server and auth-server containers each have **zero restarts**.

Restricted artifacts: `/tmp/dev-box-issue11-live.8S3nGu/` (original/reviewed/final values/manifests,
rollback package, image provenance, pod/object/UID inventories, policy/lifecycle/authorization logs,
non-content mount results, isolated fixture driver/results/cleanup and effective-config outputs).
Local preflight logs: `/tmp/dev-box-issue11-preflight.TMemlp/`. Sensitive exports stay local and
are not included here; this report contains no private key, authentik token or Secret data.
