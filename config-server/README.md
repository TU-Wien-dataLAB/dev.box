# config-server — pod templates + persistent boxes for ContainerSSH

A tiny Go server implementing the ContainerSSH **configuration webhook** protocol. It is built on
the official module (`go.containerssh.io/containerssh`, `config/webhook` package) — the same code
described in the official "Building a configuration webhook server" section
(https://github.com/ContainerSSH/ContainerSSH#building-a-configuration-webhook-server).

It serves **pod templates selected by the SSH username** from a directory that is normally fed by a
mounted Kubernetes **ConfigMap**. `ssh ubuntu@host` → the template named after the username
(`/config/ubuntu.yaml`). No arbitrary per-user configs — just a shared set of named pod templates.
The sole reserved exception is `settings`: its entire application response is synthesized in code,
without reading or rendering a template file.

In **persistent mode** (`CONTAINERSSH_OPERATING_MODE=persistent`, the dev.box target) it also gives
each authenticated user **one stable box per template**: it injects a deterministic, collision-
resistant DNS-1123 pod name (`kubernetes.pod.metadata.name`) and the `dev.box/owner` label derived
from the *authenticated* identity, and enforces the per-user box cap (see
[Persistent mode](#persistent-mode-devbox-target)).

```
                      1. POST / (JSON metadata: username, authenticatedUsername, ...)
ssh ubuntu@host → ContainerSSH ──────────────────────────────────────────────────────→ config-server
                       ←───── 200 {"config": <the "ubuntu" template, name/label injected>} ───
                                        (merged over ContainerSSH's base config)
```

- Lookup: `/config/<username>.yaml`, then `/config/<username>.json`. No match returns an error
  and denies the SSH connection in every execution mode. There is no catch-all or base-config
  fallback. `default` is an ordinary template name, used only by `ssh default@host`.
  Empty or unsafe usernames are rejected, not rewritten into another template name. `settings`
  bypasses file lookup and always selects the code-owned application; a file cannot override it.
- Files contain **partial** `AppConfig`s (a `kubernetes.pod` override), optionally wrapped with
  top-level `volumeClaimTemplates` consumed only by this server — fields you leave out are
  inherited from ContainerSSH's base config (the chart's `config.yaml`). ContainerSSH merges with
  `mergo.WithOverride`, so unset/empty fields never clobber the base. The injected persistent pod
  name/label ride the same merge path, so the strict-YAML duplicate-key pitfall in rendered
  `config.yaml` does not apply.
- File changes are picked up automatically (ConfigMap volumes are synced by the kubelet; the server
  caches parsed templates keyed on file mtime).
- The protocol is fail-closed on the ContainerSSH side: if the server returns non-200 or errors,
  the SSH connection is denied **after retries**, not immediately. ContainerSSH v0.6 retries config
  errors every ten seconds until timeout, then closes SSH without showing the template error.
  With both bundled servers enabled, the auth-server checks the chart's template-name allowlist
  during authorization instead, giving unknown names an immediate SSH `Permission denied`.

## Persistent mode (dev.box target)

With `CONTAINERSSH_OPERATING_MODE=persistent` (the bundled chart renders it from
`kubernetes.mode`) the server operates as the box identity provider:

- **Canonical identity**: the box is keyed on `authenticatedUsername` from the config request —
  v0.6 always transmits it — *never* the client-chosen SSH username. An empty authenticated
  identity is denied (fail closed).
- **One box per (owner, template)**: the matching template name is combined with the owner into
  a deterministic, collision-resistant, DNS-1123 pod name
  (`box-` + first 10 hex of SHA-256 over owner and template). Unknown usernames are denied before
  pod listing or name injection, and two users typing the same username never share a pod.
- **Owner label**: every persistent pod is labelled `dev.box/owner` = a deterministic, valid
  Kubernetes label value derived from the authenticated username (verbatim for normal usernames,
  sanitized + hashed otherwise) — used for audit and cap counting.
- **Box cap**: `CONTAINERSSH_MAX_PODS_PER_USER` (default 3) limits live boxes per owner. The
  server lists the owner's pods by label (existence and count come from one call) and denies **only
  if** the target pod does not exist **and** the count is at the cap — reconnects always pass.
  "Live" means a non-terminating pod outside the terminal `Succeeded`/`Failed` phases (`Pending`,
  `Running`, and `Unknown` still count). A listing error is denied (fail closed). The
  list-then-create race is accepted (self-inflicted, rare, self-correcting). No pods are ever
  deleted by this server; cleanup is explicit and manual.
- **Mode-aware injection**: persistent responses inject both the fixed pod name and
  `createMissingPods: true`. Injection is skipped for a template that explicitly selects a
  non-persistent execution mode. The chart always omits base `createMissingPods`: ContainerSSH's
  merge cannot replace a true boolean with false, so request-scoping it keeps the always-available
  connection-mode settings response valid as well as ordinary mixed-mode templates. External
  gateway configurations must omit the base flag too.

## Managed retained claims (template reference)

Storage is per-template opt-in, off unless `volumeClaimTemplates` is present. The envelope is
StatefulSet-style vocabulary owned by config-server, **not** a PodSpec or ContainerSSH field.
Only the ordinary `AppConfig` is returned over the webhook. Both the server operating mode and
an explicit template mode must be persistent; mounted connection/session/invalid overrides deny.

Standalone mounted template example (the chart accepts the equivalent fields under each
`kubernetes.podTemplates` item; see [chart authoring](../charts/containerssh/README.md)):

```yaml
volumeClaimTemplates:
  - metadata: {name: home}
    spec:
      accessModes: [ReadWriteOnce]
      resources: {requests: {storage: 25Gi}}
kubernetes:
  pod:
    spec:
      securityContext: {fsGroup: 1000}
      volumes:
        - name: home
          persistentVolumeClaim: {claimName: home}
      containers:
        - name: shell
          image: containerssh/containerssh-guest-image
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: {drop: [ALL]}
          resources: {limits: {memory: 128Mi, cpu: 500m}}
          volumeMounts: [{name: home, mountPath: /home}]
          env: [{name: HOME, value: /home}]
```

Claim names must be unique DNS-1123 labels and requests must specify a positive storage quantity.
PVC specs use native Kubernetes fields; storage class selection follows Kubernetes defaults when
omitted. Matching symbolic `claimName` values become `<box-name>-<claim-name>`, per authenticated
owner + resolved template + claim. Unmatched references pass through as operator-managed PVCs.
Mount paths, HOME and fsGroup have **no injected defaults**: absent fsGroup, a fresh volume may be
root-owned and unwritable for UID 1000. Duplicate volume names or mount paths are left to Kubernetes
pod validation. A nonempty `containers` slice replaces the base slice, rather than merging
individual containers: image, container security settings and resource limits must be restated.
The real-merger gate caught and corrects the issue comment's element-wise-merge assumption.

Identity and the live-box cap are checked before any claim API access. Claims are independently
created with no owner references; errors (including quota exhaustion) deny the connection without
rolling back earlier claims. Retry reuses verified claims. Both labels and exact identity
annotations use `dev.box/owner`, `dev.box/template`, `dev.box/claim`; owner/template label values
use the same valid-label encoding as pods, while annotations preserve exact canonical strings.
Existing claims, including create races returning AlreadyExists, must match all identity metadata
and have no deletion timestamp or owner references. A matching short name alone is insufficient.
The cached PodSpec is deep-copied before request-specific reference rewriting.

Only PVC `get/create` in the session namespace are required in addition to the existing server
permissions. Settings receives **no PVC permissions**, no storage-detection state and no storage
management actions. Claim specs are never reconciled or resized; existing pods are never changed.
Retained/orphan claims do not count toward the live-box cap. The chart's conditional namespace
ResourceQuota is the aggregate storage ceiling, not a per-owner limit; template count is not a
lifetime claim ceiling. Renamed/removed templates and claims leave retained orphans for explicit
operator cleanup by labels. Expansion is a manual operator patch when the StorageClass allows it.

Data in retained PVC mounts survives pod deletion/recreation; container writable layers and
`emptyDir` do not. PVC deletion is the intended operator data-destruction path (the verified Cinder
class has reclaim policy `Delete`). Namespace deletion, storage failure and users deleting files
can also lose data. Backups/snapshots and automatic GC are out of scope. The
[operations guide](../docs/operations.md) covers storage activation safety and staged
SSH/UI acceptance.

## Reserved settings application

`settings` is always reserved and synthesized by `settings.go`, never loaded from
`settings.yaml`/`.json`. The chart rejects a settings pod-template item and exposes only
`settings.image.repository/tag` for its image. With both bundled servers enabled, the template
authorization allowlist automatically includes `settings`, even with no ordinary templates.
Unknown/empty names still deny; no catch-all or `default` fallback is introduced.

The response uses connection mode, one non-root application container, read-only root, dropped
capabilities, no privilege escalation, resource bounds, and **no RuntimeClass** (no Kata). It forces
`/usr/local/bin/dev-box-settings` directly without a shell wrapper, permits only terminal-backed
application-shell startup, denies exec/subsystem/env/signal/forwarding, and allows one session
channel per transport. The static TUI, image contract, resize and q-only behavior are unchanged.

`DEV_BOX_AUTHENTICATED_OWNER` receives the **raw** verified `authenticatedUsername`; only the TUI
sanitizes it for display. `DEV_BOX_BOX_TEMPLATES` receives the owner's current ordinary
**persistent** template names, comma-separated and sorted — the same single walk that computes the
owner Role's exact `resourceNames`, so the management UI's row list and its authorization can never
diverge. An empty list is valid (no persistent templates); unreadable or unparsable template files
deny the settings response instead of serving a stale list. Empty/whitespace-only identities deny,
with no fallback to the SSH login, client env, original command or template data. Responses are
independent across concurrent owners. Settings skips persistent naming/cap listing and carries no
`dev.box/owner` label; ordinary identity, cap, labels and reconnect behavior are unchanged.

The shared base must remain restricted: only `containers`, `securityContext`, `restartPolicy`,
`terminationGracePeriodSeconds` at pod-spec level. Base pod securityContext permits only
`runAsNonRoot`, `runAsUser`, `runAsGroup`, `seccompProfile.type`; base metadata permits only
labels/annotations without `dev.box/owner`. Empty/false webhook values cannot clear unsafe inherited fields. The chart enforces this unconditionally and
injects Kata per ordinary named template, not into the shared base or settings. External base
configs need the same audit. Heavy fields remain available in ordinary templates.

**Stock v0.6 is insufficient for the full SSH policy.** Fork
[PR #11](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/11) provides the Settings fixes.
The chart pins a fork image that also includes PR #10's auth denial banners; stock v0.6
rejects the auth `message` field. Preserve the tested gateway digest when upgrading.
Gateway builds and fixes belong in the fork, not this repository.

### Per-owner Kubernetes authorization

Each settings request reconciles one ServiceAccount, Role and RoleBinding in the session
namespace. All three share the deterministic name `dev-box-owner-` + first **48 hexadecimal
characters** of SHA-256 over `strings.TrimSpace(authenticatedUsername)` (62 characters total).
This matches persistent-owner canonicalization, not display sanitization. No client-supplied
object or pod names enter authorization.

The server recomputes a sorted set of exact persistent pod names from the **current mounted
ordinary template list**, using the existing `box-` + first 10 hex of SHA-256 over trimmed owner +
NUL + resolved template. Settings and connection/session templates are excluded. When the server
operates outside persistent mode there are no persistent names to delegate. The Role allows
**only `get` and `delete` on core pods with these `resourceNames`**: no list/watch, exec/log or
foreign-owner access. No persistent templates means **zero rules**; empty `resourceNames` would
mean all pod names and must never be used to represent no access.

Reconciliation is idempotent on settings connections, not a background controller. Template
additions/removals take effect in the Role at the next settings request; existing pods are not
changed or deleted. Template read/parse errors, API failures, and unexpected immutable RoleBinding
references deny settings rather than return a credential with unverified grants. This is not a
transactional rollback or immediate revocation of prior grants after an unsuccessful reconcile.

The ServiceAccount defaults `automountServiceAccountToken: false`; the synthesized settings **pod**
sets it **true** and names that account. Kubelet mounts its scoped projected token and CA at
`/var/run/secrets/kubernetes.io/serviceaccount`. The management TUI reads box state and deletes
boxes through exactly this projected token — `get`/`delete` on the deterministic names above —
nothing wider. Three static objects persist per owner, with nothing per connection and no stored
token Secret. There is no automatic GC; owner retirement/cleanup is an explicit operator decision,
including active sessions and credentials, not an SSH disconnect or Helm uninstall side effect.

The config server is now trusted to reconcile namespace authorization objects, not just list
pods. Required namespaced permissions are core pods `list/get/delete`, core serviceaccounts
`get/list/create/update`, and RBAC roles/rolebindings `get/list/create/update`. No `bind`/`escalate`
permission is granted. Kubernetes anti-escalation checks require the server itself to hold pod
get/delete before delegating them; create/get/list alone cannot implement reconcile (update is
required). A compromised config server can affect namespace pods and these authorization objects;
the owner credential is narrower than the server credential.

**NetworkPolicy: settings reaches exactly the Kubernetes API.** The chart renders one scoped
exception (`networkPolicy.settingsApiEgress`, see the [chart README](../charts/containerssh/README.md))
allowing settings pods (`dev.box/application=settings`) to reach the Kubernetes API; ordinary pods
keep deny-ingress/egress-internet-only. Operator-side impersonation proves API authorization
independently of networking: mounted-token evidence and a live settings-pod API request are distinct
seams, and both are approval-gated. Follow
[the operations guide](../docs/operations.md).

## Build and test locally

From `config-server/`:

```bash
go build ./...
go vet ./...
go test -race ./...
```

These are offline checks: ordinary HTTP handler tests use template/cap fixtures without a real
API, and owner-RBAC tests use fake/in-memory Kubernetes clients. They do not prove live API
authorization, token mounting, networking or SSH policy.

`main` requires **`CONTAINERSSH_SETTINGS_IMAGE` and an in-cluster Kubernetes client in every
operating mode**, even with the persistent cap disabled. A bare host-side `go run .` is no longer
a supported local webhook server; setting `KUBECONFIG` does not replace `rest.InClusterConfig`.
For interactive settings testing, obtain separate approval to use the deployed service, then
forward the existing gateway and authenticate normally:

```bash
kubectl --context container-ssh --namespace containerssh port-forward svc/containerssh 2222:2222
# In another terminal; retain independently verified host identity:
ssh -t -o PreferredAuthentications=publickey -o IdentitiesOnly=yes \
  -i ~/.ssh/slurm_tu_wien -p 2222 settings@localhost
```

This creates a disposable settings pod and reconciles owner RBAC: it is a **live mutation**, not
an offline smoke test. Follow the staged approval plan first. A direct webhook POST through a
config-service port-forward trusts operator-supplied metadata; it does not prove SSH authentication
and settings requests can create/update authorization objects. Keep such fixtures separately
approved and isolated; never label synthetic owners as real authenticated-owner evidence.

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `CONTAINERSSH_CONFIG_DIR` | `/config` | Directory with `<username>.yaml` / `<username>.json` template files |
| `CONTAINERSSH_LISTEN` | `0.0.0.0:8080` | Listen address |
| `CONTAINERSSH_LOG_LEVEL` | `6` | Syslog-style: `7` debug, `6` info, `5` notice, `4` warning, `3` error, `2` crit |
| `CONTAINERSSH_OPERATING_MODE` | `connection` | `connection`, `session` or `persistent`. Only `persistent` enables name/label injection + the cap (see above) |
| `CONTAINERSSH_SESSION_NAMESPACE` | `containerssh-sessions` | Namespace of backend pods, managed PVCs and per-owner authorization objects |
| `CONTAINERSSH_SETTINGS_IMAGE` | required | Full settings guest image reference; startup fails if absent, in every mode; pin immutably for rollout |
| `CONTAINERSSH_MAX_PODS_PER_USER` | `3` | Per-owner live-box cap; `0` disables the cap. Only enforced in persistent mode |
| `CONTAINERSSH_TLS_CERT` | — | Server certificate (file path or PEM) → enables HTTPS |
| `CONTAINERSSH_TLS_KEY` | — | Server private key (file path or PEM) |
| `CONTAINERSSH_TLS_CLIENTCA` | — | CA to verify clients (enables mTLS) |

## Deploy in Kubernetes

Enable it from the ContainerSSH chart
(`configServer.enabled=true`, templates in `kubernetes.podTemplates`) — it deploys this server,
renders ordinary templates into a ConfigMap (keys = `<name>.yaml`), mounts it at `/config`,
passes `settings.image` as `CONTAINERSSH_SETTINGS_IMAGE`, and auto-wires `configserver.url`.
There is no settings template file. The server needs in-cluster credentials in every mode.

The repo CI (`.github/workflows/config-server-image.yml`) builds this directory and publishes it
to `ghcr.io/tu-wien-datalab/dev.box/config-server` on every push to `main`; the chart's
`configServer.image` defaults to that image. For the cluster to pull it, either make the GHCR
package public or add a GHCR `imagePullSecret` (set `imagePullSecrets` in the chart).

For a standalone deployment: build the image, push it, then `kubectl apply` a Deployment + Service
that mounts a ConfigMap at `/config`, configures `CONTAINERSSH_SETTINGS_IMAGE`, and runs with its
own in-cluster ServiceAccount. Bind that account to the session-namespace permissions in
[Per-owner Kubernetes authorization](#per-owner-kubernetes-authorization), including pod list for
the persistent cap and get/delete for delegation. Replicate the safe-base and per-template runtime
contracts in external gateway configuration. Missing permissions fail closed; a list-only legacy
account is no longer sufficient for settings.

## Security

For production traffic, serve HTTPS and optionally require client certificates — set
`CONTAINERSSH_TLS_CERT`/`_KEY`/`_CLIENTCA` and mount cert files, then mirror them in the chart as
`configserver.cacert` (+ `configserver.cert`/`configserver.key` for mTLS). Responses carry pod
settings, so keep it TLS/mTLS rather than plain HTTP.

## Design notes

- **Deterministic per user**: ordinary requests require exact named templates; settings is the
  single code-owned exception. Persistent box identity is (authenticated owner, resolved template).
  Settings reconciles shared namespaced authorization objects with conflict retries, without a
  controller or per-connection object growth.
- **Fail-closed selection**: missing templates return an error rather than granting access to a
  default image. Authentication/authorization still belongs to the auth server. Template errors
  and persistent-mode denials (empty identity, at-cap new box, pod-list failure) are surfaced by
  ContainerSSH as its generic "Cannot authenticate at this time" after config-request retries.
- **Persistent = pod lifecycle, not storage**: the pod survives disconnects and is never deleted by
  this server, but its writable layer dies with pod deletion/eviction/node loss. Data that must
  survive needs a PVC or another durable store. Template changes also don't mutate an
  already-created persistent pod — the deterministic name is what makes that safe: recreate or
  migrate the pod explicitly.
