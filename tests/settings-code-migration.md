# How to migrate Settings to code and accept scoped-owner authorization

Scope: [dev.box issue #11](https://github.com/TU-Wien-dataLAB/dev.box/issues/11), before
parent #7's management actions. Migrate the existing release from a values-defined Settings POC
to the code-owned application without replacing ordinary boxes, credentials or network policy.
The TUI and guest-image contract remain unchanged; no management action is added.

**Approval boundary:** this is a procedure, not rollout approval or evidence. Complete local
checks first. Obtain separate explicit approval before cluster observations/exports, port-forwards,
SSH connections, upgrades, authorization probes, fixtures or cleanup. This documentation change
performs none of them. Mark every gate PASS, FAIL, BLOCKED or NOT RUN with its actual seam.

For field/security reference, see the [chart contracts](../charts/containerssh/README.md#reserved-settings-application-and-owner-authorization)
and [config-server authorization contract](../config-server/README.md#per-owner-kubernetes-authorization).
Issue #9 [fixture evidence](settings-fixture-validation.md) is historical, not proof of issue #11.

## 1. Prepare immutable images and local evidence

1. Obtain a published immutable config-server image containing settings synthesis and owner-RBAC
   reconciliation. Keep the existing Settings guest image unchanged, but obtain its full registry
   digest for the new `settings.image` values. Verify source revision/platform and image provenance;
   a local Docker image ID is not a registry digest.
2. Keep the release's gateway reference pinned. Fork
   [PR #11](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/11) merged `settings-change` → `deploy`
   on **2026-10-16**, and deploy-line [Docker Image run 38917607974](https://github.com/TU-Wien-dataLAB/ContainerSSH/actions/runs/38917607974)
   succeeded. Chart 0.3.3 pins the PR #10 gateway digest in `charts/containerssh/values.yaml`
   for both Settings fixes and auth denial banners; unpatched `deploy` rejects the new auth
   `message` field. The chart default is **not** an instruction to change an existing deployment's image.
   The issue's starting baseline, revision **38**, retained
   `ghcr.io/tu-wien-datalab/containerssh:pr-11@sha256:529a4f5618be89a56e3714fe9f77cf886a6674131e0cec5f5f1b28841d92beb9`.
   Capture the actual approved target before proceeding; preserve its pin rather than restoring
   an older baseline.
3. Have the implementation owner run the existing/extended local seams from the repo root:

   ```bash
   (cd config-server && go build ./... && go vet ./... && go test -race ./...)
   (cd auth-server && go build ./... && go vet ./... && go test -race ./...)
   (cd settings-tui && go build ./... && go vet ./... && go test -race ./...)
   helm dependency build charts/containerssh
   tests/chart-auth-rendering.sh
   tests/chart-template-authorization.sh
   tests/chart-service-selector.sh
   tests/chart-kata-rendering.sh
   tests/chart-persistent-rendering.sh
   tests/chart-settings-rendering.sh
   # Select the reviewed gateway reference explicitly, not an accidental local default:
   CONTAINERSSH_IMAGE='<reviewed gateway reference>' tests/chart-config-binary-validation.sh
   docker build --platform linux/amd64 -t config-server:issue11 config-server
   docker build --platform linux/amd64 -t dev-box-settings:issue11 settings-tui
   python3 tests/settings-image-smoke.py --image dev-box-settings:issue11
   ```

4. Require coverage for synthesis with no settings file (including an attempted file override),
   auto-appended settings allowlist, unknown/empty-name denial, unconditional base rejection,
   ordinary-template Kata injection/overrides and absent base/settings RuntimeClass. Test the
   effective settings and ordinary configs through the **real merge and backend/security
   validators**, not only `--dump-config` parsing. Persistent creation flags must not leak into
   settings. Retain ordinary cap/reconnect behavior and the unchanged TUI terminal/image checks.
5. Require webhook/RBAC tests for raw verified-owner env, missing/blank identity, concurrent
   request isolation, deterministic `dev-box-owner-` + 48-hex names, idempotency, template
   addition/removal, zero persistent templates yielding **zero rules**, and API/read/parse failures
   denying settings. Check ServiceAccount automount false versus settings-pod override true.
   Include create/update failures and unexpected immutable RoleBinding references. Offline fake
   API tests do not establish actual Kubernetes anti-escalation/binding behavior.
6. Record actual invocations, revisions, image IDs/digests, exits and limitations in
   [the local evidence](settings-code-local-validation.md). Leave unexecuted checks
   unclaimed. Pause for review before any live work.

`config-server/main` requires `CONTAINERSSH_SETTINGS_IMAGE` and an in-cluster client **in every
mode**. Do not substitute the old host-side `go run .` example; use offline handler tests or,
after approval, the deployed gateway via port-forward. Direct settings webhook fixtures reconcile
RBAC and are mutations, not harmless synthetic-owner reads.

## 2. Export and transform user values securely (after approval)

1. Record the approval, operator, context, release revision and cleanup/time bounds. Warn that
   gateway rollouts can interrupt SSH clients. Prepare a secure local directory outside the repo:

   ```bash
   umask 077
   work=$(mktemp -d "${TMPDIR:-/tmp}/dev-box-issue11.XXXXXX")
   helm get values containerssh --kube-context container-ssh --namespace containerssh \
     --output yaml > "$work/original-user-values.yaml"
   helm get manifest containerssh --kube-context container-ssh --namespace containerssh \
     > "$work/original-manifest.yaml"
   cp "$work/original-user-values.yaml" "$work/reviewed-user-values.yaml"
   kubectl --context container-ssh --namespace containerssh-sessions get pods -o json \
     > "$work/original-pods.json"
   ```

   Exports/manifests can contain credentials: keep mode 0600, do not print/commit/upload them,
   and publish only redacted comparisons. Preserve the original chart artifact/revision and
   rollback values securely. Record existing ordinary pod names, UIDs, owner labels, runtime,
   readiness and image IDs, plus the release's image pins and Secret references. Inventory any
   pre-existing owner objects before attributing new ones to this migration.
2. In the copied **user values** remove only the `name: settings` item from
   `kubernetes.podTemplates`. Keep every ordinary item, its order, mode, metadata, security and
   spec exactly as exported. Helm replaces lists; never replace the list with a minimal example.
   An empty ordinary list is valid and still admits settings, not arbitrary names.
3. Move the removed item's unchanged guest image to `settings.image.repository/tag`, retaining
   its registry digest. Example shape (replace placeholders; this is not a complete values file):

   ```yaml
   settings:
     image:
       repository: ghcr.io/tu-wien-datalab/dev.box/settings-tui
       tag: "sha-<existing-commit>@sha256:<verified-existing-digest>"
   ```

   Alternatively, `settings.image.tag: "sha256:<digest>"` renders `repository@sha256:<digest>`.
   Inspect the rendered `CONTAINERSSH_SETTINGS_IMAGE` and require the same full pinned reference.
   Do not leave a second hand-authored settings pod spec or policy in values.
4. Select the newly built immutable `configServer.image`. Explicitly preserve the actual gateway
   digest pin, auth-server image, host-key/authentik Secret references and all unrelated values.
   Do not silently adopt the moving `deploy` tag during this migration.
5. Inspect shared `kubernetes.pod.spec`. Only `containers`, `securityContext`, `restartPolicy` and
   `terminationGracePeriodSeconds` are allowed unconditionally. Remove base RuntimeClass; ordinary
   chart templates receive Kata automatically unless they specify another class. If the old base
   had an explicit runtime override, preserve that selection **per ordinary template**.
   Move volumes, init/ephemeral containers, service accounts, automount, host namespace settings
   and other heavy fields to the ordinary templates that require them. Base pod securityContext
   allows only `runAsNonRoot`, `runAsUser`, `runAsGroup`, `seccompProfile.type`; relocate other
   subfields too. Base metadata allows only labels/annotations without `dev.box/owner`;
   remove fixed names and other fields. If this relocation changes an ordinary item, treat it as a separately
   reviewed preservation of its **effective** configuration, not permission to simplify the item.
   Audit an external `existingConfigMap` by the same rule; Helm cannot protect an external base.
6. Render the complete copied values locally into the restricted directory. Compare against the
   original manifest and review effective ordinary responses, not just YAML fragments. Require:
   no settings file/item; allowlist ordinary names plus settings; pinned settings/config/gateway
   references; safe shared base with no runtime/creation flag leakage; ordinary per-template Kata;
   scoped config-server RBAC and startup image env. Run real merge/binary validation on these
   reviewed configs. Confirm **no NetworkPolicy, Secret reference, host key, ordinary identity,
   cap, namespace, ingress, or ordinary pod mutation** is included. Resolve every unrelated diff.

## 3. Approve the trust upgrade and perform the rollout

Review these changes explicitly before the rollout owner executes the approved Helm upgrade:

- Config-server session-namespace Role: core pods **list/get/delete**; core serviceaccounts and
  RBAC roles/rolebindings **get/list/create/update**. No `bind`/`escalate`, wildcard or cluster-wide
  grant. The original issue's create/get/list proposal is insufficient: update enables reconcile,
  and Kubernetes requires the server to hold the get/delete pod permissions it delegates.
- Each owner gets three persistent objects, named `dev-box-owner-` + first **48 hex characters**
  of SHA-256 over the trimmed verified identity (62 characters total). Role rules grant only core
  pod get/delete on recomputed exact persistent names; zero names means zero rules, not an empty
  `resourceNames`. No list, settings/nonpersistent names, subresources or foreign-owner names.
- ServiceAccount defaults automount **false**; the settings pod explicitly sets **true** for its
  scoped projected token at `/var/run/secrets/kubernetes.io/serviceaccount`. This intentionally
  supersedes issue #9's no-token floor. The static TUI does not consume the credential.
- The config server is trusted with namespace pod deletion and RBAC reconciliation. It uses only
  verified identity and mounted operator templates. Failures deny a new settings response, but
  reconciliation is not transactional and does not revoke previously issued grants atomically.
  There is no background controller or automatic owner-object GC.

Use the reviewed full user-values file and the reviewed chart artifact; do not rely on fresh
install defaults or an unreviewed `--reuse-values` merge. Record the actual upgrade command and
result. Wait for auth/config/gateway readiness and verify startup image env/config checksums.
External ConfigMaps need an explicit separately approved restart because Helm cannot checksum them.
Do not recreate ordinary pods to test the upgrade: verify their existing UIDs remain unchanged.

## 4. Accept each seam in stages

Stop on FAIL; retain bounded diagnostics without exposing credentials. Synthetic metadata and
impersonation are supporting evidence, not two real enrolled-owner SSH acceptance (tracked in #10).

| Stage | Required observations / negative controls |
| --- | --- |
| A — Ordinary positive control | Before/after rollout and settings probes: known ordinary SSH exec/reconnect succeeds; exact pod name/UID/owner label and Kata runtime unchanged. Verify cap boundary only with separately approved isolated fixtures; settings remains excluded. Never delete a real box to reach the cap. |
| B — Settings synthesis and policy | Real enrolled-key `ssh -t settings@…` works with no settings file; exact raw verified owner feeds the display, static q-only UI unchanged. Run the existing SSH policy probe with a working ordinary control: explicit denial replies for exec/subsystem/env/signal/forwarding, repeated start and second channel (also after q on the same healthy transport); PTY/resize/quit succeed. Unknown/empty ordinary names still deny promptly through authz. Timeout, destination failure and plain EOF are not policy-denial evidence. |
| C — Effective pod/token | Observe the live disposable settings pod: unique connection name/UID, no persistent owner label or RuntimeClass, non-root/read-only/cap-drop/limits floor, expected account, pod automount true while account default is false. Inspect projected serviceAccountToken source, standard token/CA mount and file existence using approved non-content inspection. No legacy token Secret, host/authentik credentials, host mounts or ordinary PVCs. Never print token bytes; do not add a shell to the unchanged scratch guest. |
| D — API authorization | After a real settings connection creates the objects, inspect the Role and binding, then use operator impersonation below. Own exact names allow get/delete; foreign names, settings/nonpersistent pods, namespace list/watch and exec/log/secrets deny. These checks do not delete pods or prove token delivery/networking. Inventory ambient ClusterRoleBindings (including system:authenticated); report unexpected aggregate grants rather than claiming blanket isolation. |
| E — Reconciliation / fail closed | In separately approved isolated fixtures, add/remove an ordinary persistent template, reconnect settings, and require the same three object names with updated exact rules; a removed name loses authorization even if its pod remains. With zero eligible templates require zero rules and denied pod access. Test blank verified identity, unreadable/malformed mounted template, API outage and create/update permission failures: no successful settings response/new pod. Prior objects/grants can remain on failure; record that limitation. Restore fixtures and ordinary controls. |
| F — Lifetime / recovery | q ends the UI but may leave its parent transport/pod; full disconnect, abrupt-client and actual OpenSSH master cleanup use recorded UID bounds. Fresh settings connects get fresh pod UIDs while reusing the same three owner objects. Confirm no extra per-connection authorization objects and unchanged ordinary UID; stop only test transports. |

### Operator impersonation checks (authorization only)

With the separately approved operator's impersonation rights, set `OWNER_SA`, `OWN_POD`,
`FOREIGN_POD` and `SETTINGS_POD` from verified observations/recomputed names, not client input.
Model service-account groups as well as its username to expose inherited bindings:

```bash
as_owner=(--context container-ssh --namespace containerssh-sessions
  --as="system:serviceaccount:containerssh-sessions:${OWNER_SA}"
  --as-group=system:serviceaccounts
  --as-group=system:serviceaccounts:containerssh-sessions
  --as-group=system:authenticated)
kubectl "${as_owner[@]}" auth can-i get "pods/${OWN_POD}"        # yes
kubectl "${as_owner[@]}" auth can-i delete "pods/${OWN_POD}"     # yes; no deletion occurs
kubectl "${as_owner[@]}" auth can-i get "pods/${FOREIGN_POD}"    # no
kubectl "${as_owner[@]}" auth can-i delete "pods/${FOREIGN_POD}" # no
kubectl "${as_owner[@]}" auth can-i get "pods/${SETTINGS_POD}"   # no
kubectl "${as_owner[@]}" auth can-i delete "pods/${SETTINGS_POD}" # no
kubectl "${as_owner[@]}" auth can-i list pods                   # no
kubectl "${as_owner[@]}" auth can-i watch pods                  # no
kubectl "${as_owner[@]}" auth can-i create pods --subresource=exec # no
kubectl "${as_owner[@]}" auth can-i get pods --subresource=log   # no
kubectl "${as_owner[@]}" auth can-i get secrets                 # no
```

For repeated nonempty-name checks, the opt-in read-only helper covers the same impersonation seam
plus mutation/object-access negatives (after approval; it never requests a token or deletes a pod):

```bash
python3 tests/settings-owner-authorization.py \
  --context container-ssh --namespace containerssh-sessions \
  --service-account "$OWNER_SA" --own-pod "$OWN_POD" \
  --foreign-pod "$FOREIGN_POD" --settings-pod "$SETTINGS_POD"
```

Repeat `--own-pod`/`--foreign-pod` for additional observed names. Also test an observed nonpersistent
pod name, another namespace, pod logs, and zero-rule fixtures independently (the helper requires at
least one allowed own name). Record actual yes/no and exit status. A fabricated foreign name is useful supporting coverage but is
not proof involving two real enrolled owners. A denied actual DELETE is unnecessary on real data;
use authorization reviews, not destructive commands, to establish delete permission.

### Network boundary and optional mounted-token proof

**Unchanged session NetworkPolicy denies cluster API egress for settings.** DNS permission does
not permit API TCP traffic. Do not change `networkPolicy.extraEgress`, disable policy, or classify
a timeout as an RBAC denial. A real API request from the settings pod is **BLOCKED BY NETWORK
POLICY**, pending separate network-policy approval outside issue #11. The UI uses no token.

If separate approval permits a token-authentication probe without changing policy, an operator may
use an audited temporary command/tool to read the **actual kubelet-mounted token** via the operator
exec channel, keep it only in process memory, and submit non-destructive TokenReview/SelfSubjectAccessReview
or named GET requests from an operator-reachable API endpoint with CA verification. The unchanged
guest has no shell/curl; agree a suitable inspection method first, or leave this gate NOT RUN.
Never put the bearer in command arguments, logs, shell history, committed files or published output;
do not substitute a newly minted `kubectl create token` token and call it mounted-token evidence.

Record identity, requested resource, actual HTTP decision/status, expiry/audience checks and limits,
not credentials. An operator-originated request proves that token/API-auth seam only; it does not
prove settings-origin network reachability. Impersonation alone proves neither mounted-token
validity nor successful token-authenticated requests. Do not silently equate these acceptance gates.

## 5. Restore fixtures, retain explicit lifecycle control, and report

1. Restore separately approved fixtures/images/templates, disconnect only test transports, and
   require ordinary positive control with original name/UID. Confirm only test settings UIDs have
   disappeared; unrelated ordinary boxes and Secrets remain untouched. Reconcile restored templates
   by a settings connection if needed and approved.
2. Keep per-owner objects by default. For owner retirement, explicitly identify the three exact
   objects, inspect active settings sessions/bindings and approve GC individually. No broad label
   deletion, namespace purge or Helm uninstall. Pod deletion is a future owner restart primitive
   with data-loss implications, not part of this migration's acceptance.
3. If rollback is approved, use the secured original chart artifact and values together, retaining
   the previous settings item/image and gateway pin as that version requires. Account for already
   created authorization objects/active credentials separately: Helm rollback does not collect or
   revoke them. Never delete ordinary boxes to make rollback work.
4. Record release/config/source revisions, image digests, command exits, explicit policy replies,
   observed object names/rules, pod UIDs and before/after controls. Keep raw sensitive exports local;
   publish only redacted evidence. Report API authorization, mount inspection, token authentication
   and settings-origin networking separately. Mark skipped fixtures/real-owner cases NOT RUN and
   network-blocked calls BLOCKED. Pause for review; do not declare #7's actions or #10 complete.
