# config-server — username-selected pod templates for ContainerSSH

A tiny Go server implementing the ContainerSSH **configuration webhook** protocol. It is built on
the official module (`go.containerssh.io/containerssh`, `config/webhook` package) — the same code
described in the official "Building a configuration webhook server" section
(https://github.com/ContainerSSH/ContainerSSH#building-a-configuration-webhook-server).

It serves **pod templates selected by the SSH username** from a directory that is normally fed by a
mounted Kubernetes **ConfigMap**. `ssh ubuntu@host` → the template named after the username
(`/config/ubuntu.yaml`). No arbitrary per-user configs — just a shared set of named pod templates.

```
                      1. POST / (JSON metadata: username, ...)
ssh ubuntu@host → ContainerSSH ────────────────────────────────────────→ config-server
                       ←───── 200 {"config": <the "ubuntu" template>} ───
                                        (merged over ContainerSSH's base config)
```

- Lookup: `/config/<username>.yaml` (or `.json`) → fallback `/config/default.yaml` → then an empty
  config (= ContainerSSH uses its base config unchanged).
- Files are **partial** `AppConfig`s (a `kubernetes.pod` override) — fields you leave out are
  inherited from ContainerSSH's base config (the chart's `config.yaml`). ContainerSSH merges with
  `mergo.WithOverride`, so unset/empty fields never clobber the base.
- File changes are picked up automatically (ConfigMap volumes are synced by the kubelet; the server
  caches parsed templates keyed on file mtime).
- The protocol is fail-closed on the ContainerSSH side: if the server returns non-200 or errors,
  the SSH connection is denied.

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
that mounts a ConfigMap at `/config`. No ServiceAccount or RBAC is needed — the server reads a
mounted volume, not the API.

## Security

For production traffic, serve HTTPS and optionally require client certificates — set
`CONTAINERSSH_TLS_CERT`/`_KEY`/`_CLIENTCA` and mount cert files, then mirror them in the chart as
`configserver.cacert` (+ `configserver.cert`/`configserver.key` for mTLS). Responses carry pod
settings, so keep it TLS/mTLS rather than plain HTTP.

## Design notes

- **Deterministic per user**: the lookup is `<username>.yaml` → `default.yaml` → base. The server
  is stateless and replica-safe (no coordination, caching, or shared state).
- **"default" is the catch-all**: name one template `default` to give unmatched users a pod; leave
  it out to fall back to the base pod for unknown users.
- **Rejecting users is not its job**: this is the *config* server. Authentication/authorization
  belongs to the auth server; return errors here only for genuine server-side problems.
- **Persistent mode is the dev.box target, not current behavior**: ContainerSSH v0.6 requires an
  exact `kubernetes.pod.metadata.name` in persistent mode. The planned config-server change must
  derive a stable, collision-resistant DNS-1123 pod name from the canonical authenticated identity
  (`authenticatedUsername`) and return it with `mode: persistent` and `createMissingPods: true`. Today this server only returns static templates,
  and the chart does not render `createMissingPods`; setting `kubernetes.mode=persistent` alone is
  therefore not a working persistent deployment.
