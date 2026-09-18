# config-server — pod templates + persistent boxes for ContainerSSH

A tiny Go server implementing the ContainerSSH **configuration webhook** protocol. It is built on
the official module (`go.containerssh.io/containerssh`, `config/webhook` package) — the same code
described in the official "Building a configuration webhook server" section
(https://github.com/ContainerSSH/ContainerSSH#building-a-configuration-webhook-server).

It serves **pod templates selected by the SSH username** from a directory that is normally fed by a
mounted Kubernetes **ConfigMap**. `ssh ubuntu@host` → the template named after the username
(`/config/ubuntu.yaml`). No arbitrary per-user configs — just a shared set of named pod templates.

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

- Lookup: `/config/<username>.yaml` (or `.json`) → fallback `/config/default.yaml` → then an empty
  config (= ContainerSSH uses its base config unchanged). In persistent mode the resolved template
  (including the `default` catch-all) is part of the box identity.
- Files are **partial** `AppConfig`s (a `kubernetes.pod` override) — fields you leave out are
  inherited from ContainerSSH's base config (the chart's `config.yaml`). ContainerSSH merges with
  `mergo.WithOverride`, so unset/empty fields never clobber the base. The injected persistent pod
  name/label ride the same merge path, so the strict-YAML duplicate-key pitfall in rendered
  `config.yaml` does not apply.
- File changes are picked up automatically (ConfigMap volumes are synced by the kubelet; the server
  caches parsed templates keyed on file mtime).
- The protocol is fail-closed on the ContainerSSH side: if the server returns non-200 or errors,
  the SSH connection is denied.

## Persistent mode (dev.box target)

With `CONTAINERSSH_OPERATING_MODE=persistent` (the bundled chart renders it from
`kubernetes.mode`) the server operates as the box identity provider:

- **Canonical identity**: the box is keyed on `authenticatedUsername` from the config request —
  v0.6 always transmits it — *never* the client-chosen SSH username. An empty authenticated
  identity is denied (fail closed).
- **One box per (owner, template)**: the resolved template name (`<username>.yaml` →
  `default.yaml`; the `default` catch-all if none matches) is combined with the owner into a
  deterministic, collision-resistant, DNS-1123 pod name
  (`box-` + first 10 hex of SHA-256 over owner and template). Unknown usernames collapse onto the
  same `default` box instead of minting unbounded pods, and two users typing the same username
  never share a pod.
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
  non-persistent execution mode. The chart normally renders base `createMissingPods: true`, but
  omits it when chart-managed templates include a connection/session override: ContainerSSH's
  merge cannot replace a true boolean with false, so request-scoping it in mixed-mode deployments
  keeps those templates valid.

## Build & run locally

```bash
go build -o config-server .          # or: go run .

mkdir -p /tmp/pods
cat > /tmp/pods/ubuntu.yaml <<'YAML'
kubernetes:
  pod:
    spec:
      containers:
        - name: shell
          image: ubuntu:22.04
          command: ["/bin/bash"]
YAML

CONTAINERSSH_CONFIG_DIR=/tmp/pods \
CONTAINERSSH_LISTEN=127.0.0.1:8080 \
CONTAINERSSH_LOG_LEVEL=6 ./config-server

# as user "ubuntu":
curl -s -X POST http://127.0.0.1:8080/ \
  -H 'Content-Type: application/json' \
  -d '{"username":"ubuntu","connectionId":"c1","remoteAddress":"10.0.0.1:43210"}'
```

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `CONTAINERSSH_CONFIG_DIR` | `/config` | Directory with `<username>.yaml` / `default.yaml` template files |
| `CONTAINERSSH_LISTEN` | `0.0.0.0:8080` | Listen address |
| `CONTAINERSSH_LOG_LEVEL` | `6` | Syslog-style: `7` debug, `6` info, `5` notice, `4` warning, `3` error, `2` crit |
| `CONTAINERSSH_OPERATING_MODE` | `connection` | `connection`, `session` or `persistent`. Only `persistent` enables name/label injection + the cap (see above) |
| `CONTAINERSSH_SESSION_NAMESPACE` | `containerssh-sessions` | Namespace of the backend user pods (cap listing) |
| `CONTAINERSSH_MAX_PODS_PER_USER` | `3` | Per-owner live-box cap; `0` disables the cap. Only enforced in persistent mode |
| `CONTAINERSSH_TLS_CERT` | — | Server certificate (file path or PEM) → enables HTTPS |
| `CONTAINERSSH_TLS_KEY` | — | Server private key (file path or PEM) |
| `CONTAINERSSH_TLS_CLIENTCA` | — | CA to verify clients (enables mTLS) |

## Deploy in Kubernetes

Enable it from the ContainerSSH chart
(`configServer.enabled=true`, templates in `kubernetes.podTemplates`) — it deploys this server,
renders the templates into a ConfigMap (keys = `<name>.yaml`), mounts it at `/config`, and
auto-wires `configserver.url`.

The repo CI (`.github/workflows/config-server-image.yml`) builds this directory and publishes it
to `ghcr.io/tu-wien-datalab/dev.box/config-server` on every push to `main`; the chart's
`configServer.image` defaults to that image. For the cluster to pull it, either make the GHCR
package public or add a GHCR `imagePullSecret` (set `imagePullSecrets` in the chart).

For a standalone deployment: build the image, push it, then `kubectl apply` a Deployment + Service
that mounts a ConfigMap at `/config`. **Persistent mode needs list access to pods in the session
namespace** so the cap can run: give the Deployment its own ServiceAccount bound to a Role limited
to `list` on `pods` in that namespace (the bundled chart does exactly this; standalone operators
must replicate it or every connection is denied fail-closed when the listing fails — no pod ever
is).

## Security

For production traffic, serve HTTPS and optionally require client certificates — set
`CONTAINERSSH_TLS_CERT`/`_KEY`/`_CLIENTCA` and mount cert files, then mirror them in the chart as
`configserver.cacert` (+ `configserver.cert`/`configserver.key` for mTLS). Responses carry pod
settings, so keep it TLS/mTLS rather than plain HTTP.

## Design notes

- **Deterministic per user**: the lookup is `<username>.yaml` → `default.yaml` → base. In persistent
  mode the box identity is (authenticated owner, resolved template) — deterministic and
  replica-safe (no coordination or shared state beyond the stateless pod-listing call).
- **"default" is the catch-all**: name one template `default` to give unmatched users a pod; leave
  it out to fall back to the base pod for unknown users. Either way, in persistent mode unknown
  usernames resolve to one shared `default` box per authenticated owner.
- **Rejecting users is not its job**: this is the *config* server. Authentication/authorization
  belongs to the auth server. Errors here are for genuine server-side problems **plus** the
  persistent-mode denials (empty identity, at-cap new box, pod-list failure) that ContainerSSH
  surfaces as its generic fail-closed "Cannot authenticate at this time".
- **Persistent = pod lifecycle, not storage**: the pod survives disconnects and is never deleted by
  this server, but its writable layer dies with pod deletion/eviction/node loss. Data that must
  survive needs a PVC or another durable store. Template changes also don't mutate an
  already-created persistent pod — the deterministic name is what makes that safe: recreate or
  migrate the pod explicitly.
