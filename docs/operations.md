# Operate and accept the dev.box release

Use this runbook for approved changes and acceptance, not as authorization to access the cluster.
**Read [recorded deployment status](deployment-status.md) before any operational work**, especially
its exact-UID Ubuntu storage hold and unresolved acceptance gates. Recorded state is not a fresh
observation or permission to recreate a pod. Test commands live in
[tests/README.md](../tests/README.md): `./tests/check.sh` is offline; Docker/image validation and
manual live probes are separate. Component/chart READMEs define the implementation contracts.

## Prepare a safe rollout or acceptance run

1. Finish offline checks, then separately run the relevant Docker/image checks from the test
   reference against reviewed immutable images. Require real merge + backend/security validation
   and binary parsing; a built image, screenshot or parsing success is not live acceptance.
2. Obtain explicit approval **before observations/exports, port-forwards, SSH, authorization
   probes or changes**. Obtain separate approval for disruptive fixtures, upgrades, recreation,
   storage migration and cleanup. Record operator, scope, timeout/cleanup bounds and rollback plan.
3. After approval, securely export full user values/manifests and inventory pod/PVC UIDs,
   owner objects, imageIDs, readiness/restarts and policies. Keep exports outside git, mode 0600;
   publish only redacted comparisons. Preserve chart artifact/rollback values, host key,
   token Secret references, all ordinary templates and unrelated settings. Use reviewed full
   values, not an unreviewed `--reuse-values`; Helm replaces lists.
4. Render/diff locally. Check safe shared base, per-template Kata, code-owned Settings (no
   values-defined item), scoped RBAC/token, no persistent creation-flag leakage and exact
   ordinary identity. External ConfigMaps need the same audit and an approved explicit restart.
5. Every Helm operation names `--kube-context container-ssh --namespace containerssh`;
   every kubectl operation names `--context container-ssh` and its namespace. An approved
   upgrade uses the reviewed artifact/full values, bounded wait and rollback behavior. Warn
   that gateway restarts interrupt clients; wait for all three Deployments/endpoints Ready.
6. Use existing enrolled keys and independently verified known_hosts entries (including the
   loopback port alias). Do not use auto-add trust, enroll keys or alter production authentik.
   Authentik remains read-only. A deployed config request can reconcile RBAC/create PVCs:
   synthetic metadata requests are mutations, not harmless reads.

## Accept the deployed behavior

Run sequentially, stop on failure, and record each gate **PASS / FAIL / BLOCKED / NOT RUN** with
revision, source/image digests, actual replies/statuses, timing and before/after UIDs. Run the
surviving live helpers using reviewed arguments from `tests/README.md` and their `--help`.
Approval and successful historical checks do not transfer to a new run.

| Gate | Required current evidence |
| --- | --- |
| Ordinary control | Before/after every stage, enrolled-key exec/reconnect works with the same deterministic name, UID, verified-owner label and Kata runtime. Observe disconnect retention. Never recreate the real box to make a test pass. |
| Auth ordering | Positive control first; unknown key, unknown/empty/absent-default template and password deny with no new backend pod. Require prompt unknown-name denial (regression bound 5 s). Ambiguous-key and configured-group denials need isolated approved fixtures, not duplicate production keys. Group gate is NOT CONFIGURED when absent. |
| Settings synthesis/pod | Real `settings` login succeeds without a file. Verify unique connection pod/UID, expected account, direct executable, no persistent owner label/RuntimeClass, non-root/read-only/cap-drop/resources floor, no host/authentik credentials, host mounts or ordinary PVCs. Inspect effective gateway policy too. |
| UI and terminal | Verify master name/state rows, detail status/uptime/restarts and image/runtime/requests/limits, full-height panes, footer, advisory created N/max, manual refresh and ~3 s polling. Check small/plain terminals, resize, q, inert command/escape/suspend input and real terminal restoration. API failure dims rows/disables delete; 403 hints reconnect. Human visual review and instrumented side-effect absence are distinct from automated marker checks. |
| SSH policy | Run `ssh-settings-policy.py` with ordinary control, observed Settings selector `dev.box/application=settings`, explicit context/namespace, trusted host key and known owner. Require explicit failure replies for exec/SCP/subsystems/env spoof/signal/X11/agent/reverse forwarding and repeated starts; direct TCP/Unix forwarding codes 1 or 3, not destination-failure code 2; second session code 4 while active **and after q on the same healthy transport**. PTY/shell/resize succeed; non-PTY gives explanation/nonzero exit. Timeout, EOF, closed transport or fire-and-forget return is not denial evidence. Pin Paramiko 3.5.1 unless its private reply-observation seam is reviewed. |
| Lifecycle | Run `ssh-settings-lifecycle.py`: q ends application but parent/master and pod can remain; full disconnect removes that exact test UID within bounds. Kill only the probe's local client; test real OpenSSH master second-session refusal with new-connection fallback disabled. Fresh login gets a fresh Settings UID, same three owner objects. Optional resize/exit stress requires separate selection and gateway restart-count checks. |
| Management delete/recreate | Only with a separately approved disposable ordinary test box: verify exact dialog target before y; cancel does nothing. Confirmation names local-data loss/retained-PVC preservation, active-session termination and next-login recreation. Observe Terminating → not-created → SSH recreation with same name/new UID, other boxes unchanged. Never use the protected real ubuntu box for this gate without its specific approval. |
| Network | Verify Settings-only API exception matches actual apiserver topology (see recorded topology in deployment status); Calico post-NAT requires 443/6443. Prove actual Settings-origin API requests. Separately test ordinary DNS/public egress and blocked ingress/private/cluster destinations; inability to reach one destination is not blanket isolation proof. |

Drain PTY output continuously; an undrained driver can stall q/session close. Use a terminal
emulator such as `pyte` for Bubble Tea ECH/CUF differential output, not raw-line ANSI stripping;
cap text may arrive in fragments and owner clips below ~80 columns. OpenSSH `-tt` with piped
ordinary exec stdout can swallow output; do not mistake that client artifact for a backend failure.
Disabled password methods may still be advertised: publickey-only/IdentitiesOnly avoids prompts;
BatchMode also suppresses local encrypted-key prompts.

### Prove owner authorization without conflating seams

1. Observe the real Settings pod's owner SA/Role/Binding: `dev-box-owner-` + first 48 hex of
   SHA-256(trimmed verified owner). Account automount defaults false, pod true; inspect projected
   token/CA/namespace mount and file presence **without publishing credential bytes**.
2. Run `settings-owner-authorization.py` with observed own/foreign/settings names. Operator
   impersonation must include groups `system:serviceaccounts`,
   `system:serviceaccounts:containerssh-sessions`, `system:authenticated`. Require own exact pod
   get/delete only; deny foreign/settings/nonpersistent names, cross-namespace, list/watch,
   exec/log, mutations, Secrets/SA/RBAC/token issuance and PVC access. Use authorization reviews,
   not destructive DELETEs, on real data. Zero eligible templates must yield **zero rules**.
3. Inventory ambient group/ClusterRoleBinding grants, including the unresolved inherited Calico
   authority in [deployment status](deployment-status.md#recorded-authorization-and-network-evidence).
   Core pod negatives do not prove blanket least privilege or end-to-end network-policy isolation.
   Review this before enrollment.
4. Report mount presence, impersonated authorization, actual mounted-token authentication and
   Settings-origin networking separately. A successful management request is not independent
   TokenReview/audience/expiry validation or a two-owner negative test. If such a probe is approved,
   use the actual mounted bearer in memory with CA verification; never substitute
   `kubectl create token` or expose bearer bytes in args/logs/files.

### Exercise cap and fail-closed boundaries only in isolation

Use separately reviewed disposable owners/templates/permissions; never delete real boxes, change
production identities or disrupt production authentik to manufacture a boundary.

- Cap occupied: existing ordinary reconnect passes, new persistent name denies before PVC
  creation; Settings stays exempt even concurrently. Correlate config-server cause and absence
  of new pods/claims; SSH authentication alone does not prove backend success. UI count is advisory
  (published-template rows only); config-server's owner-pod list is authoritative.
- Blank verified owner, malformed/unreadable templates, API outage, list/get/create/update errors
  and removed held pod-delegation rights deny successful config/new pod. Test template add/remove
  reconciliation and zero-template zero-rule behavior; removed names lose rights even if pods
  remain. Same three owner-object identities persist. Reconciliation is not transactional:
  partial objects/old grants may remain after failure; no atomic revocation or background GC.
- Test auth/config endpoint loss one at a time only after disruptive approval; require connection
  denial/no new backend, restore readiness and positive controls each time. Never stop authentik.
- Fault-injection fixture, its publishing workflow and local gateway patch tooling are **retired**.
  Old missing-executable/TUI-crash/missing-owner-proxy fault-injection cannot be reproduced with
  the current tree. Reintroducing audited isolated instrumentation needs a new reviewed plan
  and approval; do not edit code-owned Settings policy or add a shell to bypass this limitation.
  Non-PTY failure and a killed SSH client do not substitute for startup/crash acceptance.
- For a separately approved image-upgrade gate, hold an old Settings session, select a new
  immutable image, verify a fresh connection's new UID/imageID, then end test transports and
  restore reviewed values. Existing sessions need not migrate; rollout can interrupt them.

## Activate ubuntu storage only after specific approval

1. Reobserve exact pod/PVC UIDs against the [recorded storage hold](deployment-status.md#current-baseline-and-storage-hold). Agree backup/migration and
   writable-layer data-loss disposition, downtime and explicit recreation approval first.
2. Recheck reviewed template's complete container/security/limits, /home mount, HOME, fsGroup,
   storage class, identity annotations, quota and readiness. A container slice override replaces
   the base; template authoring details are in the chart README's retained-storage contract.
3. If approved, delete only the identified pod with a UID precondition, **not its retained PVC**.
   Reconnect through real SSH; require new pod UID, same deterministic name/PVC UID, Bound 5Gi
   claim, writable /home as UID 1000, expected HOME/group/runtime. Verify migration separately.
4. Retention across another deletion/recreation needs its own approved scope: sync a disposable
   PVC marker and /tmp marker, recreate, require retained marker/same PVC and lost /tmp marker.
   No existing production files may be used as disposable test data.

Managed claims have exact owner/template/claim identity, no pod owner references; foreign,
terminating or conflicting claims deny. Reuse does not reconcile specs; partial multi-claim
failures retain successful claims for retry. Renamed/removed templates/claims accumulate; quota
is namespace-wide, not owner fairness. Expansion is deliberate operator work, not a controller.

## Close the run and retain unresolved gates

Restore only approved temporary values/images/routes/permissions; end only test transports.
Observe exact test UIDs disappearing (DELETE acceptance is not confirmed disappearance).
Readiness rollback/normal disconnect cleanup is bounded; finalizers/API/node failures and
arbitrary gateway loss can leave orphans. No background orphan collector is promised.
Delete test pods/PVCs/owner objects only with explicit scope and UID preconditions; inspect active
sessions/bindings first. Helm rollback does not revoke projected credentials or collect owner
objects/retained claims. PVC/PV disappearance is not independent Cinder-volume deletion proof.
Finish with ordinary positive control/original UID, readiness/restart/image inventory and redacted
results; retain sensitive rollback exports locally, not as repository evidence.

Carry forward every [unresolved gate](deployment-status.md#unresolved-gates) with its evidence
status. Update the dated deployment record after an approved run; historical tests are **not
current acceptance**. Never silently drop a storage hold or treat a disposable-fixture result as
acceptance for an owner's real box.
