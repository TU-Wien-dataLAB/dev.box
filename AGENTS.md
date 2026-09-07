# dev.box — AGENTS.md

Working directory for a **ContainerSSH + Kubernetes** dev box. This file gives agents and
maintainers the context needed to work here safely.

## What this is

A personal dev sandbox for running [ContainerSSH](https://containerssh.io) with the **Kubernetes
backend**: users SSH in and are dropped into ephemeral Kubernetes pods. It consists of:

1. **`charts/containerssh/`** — a Helm chart (v2, `containerssh-0.1.0`, `appVersion: 0.6`) that
   deploys ContainerSSH itself plus optional extras.
2. **`config-server/`** — a small Go server implementing the ContainerSSH config webhook protocol
   (built on `go.containerssh.io/containerssh` `config/webhook`), serving **pod templates selected
   by SSH username**.

Reference docs (both official, version 0.6):
- Installation in Kubernetes: https://containerssh.io/v0.6/getting-started/installation/ (Kubernetes tab)
- Kubernetes backend reference: https://containerssh.io/v0.6/reference/kubernetes/

Source of truth for ContainerSSH internals: `/Users/matthiasmatt/Documents/Work/oss/ContainerSSH`
(this repo is checked out next to dev.box and is frequently used to verify behavior).

> Repo: `git@github.com:TU-Wien-dataLAB/dev.box.git` (set up, empty). Nothing has been deployed
> yet — see [Deployment status](#deployment-status).

## Layout

```
dev.box/
├── AGENTS.md                  ← this file
├── charts/containerssh/       ← the Helm chart
│   ├── Chart.yaml             (name containerssh, v0.1.0, appVersion 0.6)
│   ├── values.yaml            (everything is configurable from here)
│   ├── README.md
│   └── templates/
│       ├── _helpers.tpl
│       ├── configmap.yaml     (renders ContainerSSH config.yaml from values)
│       ├── deployment.yaml    (the ssh server pod)
│       ├── ingress-tcp.yaml   (Traefik IngressRouteTCP for SSH — raw TCP, no TLS/cert-manager)
│       ├── service.yaml
│       ├── serviceaccount.yaml
│       ├── rbac.yaml          (Role/RoleBinding for session pods in the session namespace)
│       ├── networkpolicy.yaml (session pods: no ingress, egress internet-only)
│       ├── secret-hostkey.yaml
│       ├── configserver.yaml  (bundled config server: ConfigMap + Deployment + Service)
│       ├── NOTES.txt
│       └── tests/test-connection.yaml
└── config-server/             ← the config webhook server source
    ├── main.go
    ├── Dockerfile
    ├── go.mod                 (go 1.25.3, requires go.containerssh.io/containerssh v0.6.0)
    └── README.md
```

## How it fits together

```
ssh ubuntu@dev.box.example.com
        │ 1. auth (+ optional config request)
        ▼
   ContainerSSH (Deployment, port 2222)
        │ 2. POST /config (username, ip, connectionId)
        ▼
   config-server (bundled, auto-wired configserver.url)
        │ 3. reply: ONE pod template, merged over base config
        ▼
   session pod in namespace `containerssh-sessions`
        (subnet: networkpolicy = no ingress, egress to internet only)
```

- Per SSH **username** → a pod **template** with that name: `ssh ubuntu@…` → `ubuntu.yaml`.
- Template lookup: `<username>.yaml` → `default.yaml` → (server empty → base pod).
- The config-server response is **merged over the chart's base config** (see merge rule below).

## Chart key values (`charts/containerssh/values.yaml`)

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository/tag` | `containerssh/containerssh`, `v0.6` | SSH server image |
| `ssh.port` / `service.*` | `2222` / ClusterIP | SSH listener + exposure (NodePort/LB available) |
| `ingress.enabled` / `ingress.tcp.*` | `false` / `ssh` | Traefik IngressRouteTCP (raw TCP) fronts the SSH port; **no cert-manager/TLS** — SSH is not HTTP |
| `ssh.hostKey.existingSecret` / `.privateKey` | `""` | stable host key; else ephemeral key fallback |
| `auth` | password webhook, url `""` | **required** before SSH works (auth server URL) |
| `kubernetes.sessionNamespace` | `containerssh-sessions` | where per-session pods run (chart force-manages) |
| `kubernetes.pod` | security hard defaults | base/fallback pod config |
| `kubernetes.podTemplates` | `[]` | named pod templates (name = SSH username); needs `configServer.enabled` |
| `configServer.enabled` | `false` | deploy the bundled config server + auto-wire `configserver.url` |
| `configserver.*` | url `""` | client-side config-server connection (timeout/TLS/mTLS) |
| `networkPolicy.enabled` | `true` | session pods: deny ingress, egress internet-only (+ kube-dns) |
| `rbac.enabled` | `true` | Role/RoleBinding for pod management in session namespace |
| `log.level` | `5` | syslog-numbered (7 debug … 2 crit) |

## Non-negotiable invariants / gotchas

- **Log levels are syslog-numbered, higher = more verbose**: `7` debug, `6` info, `5` notice
  (default), `4` warning, `3` error, `2` crit. Do **not** write "0=trace…5=crit" anywhere.
- **The chart force-manages `kubernetes.pod.metadata.namespace` and `generateName`** in
  `configmap.yaml` (= `sessionNamespace`, `containerssh-`). Don't set them in
  `kubernetes.pod.metadata` — duplicate keys break config loading (strict YAML).
- **The config server / override merge**: ContainerSSH merges the webhook response over the base
  config via `structutils.Merge` = `mergo.Merge(dst, src, mergo.WithOverride)`. Empty/nil source
  fields are **skipped**, so partial per-template overrides are safe (proven by a mergo test); a
  template that only sets labels keeps the base pod's containers/spec.
- **Config server is fail-closed**: with `configserver.url` set, ContainerSSH *denies* connections
  when the config POST fails (retries every 10 s, non-200 = "Cannot authenticate at this time").
  Bundled server must be Ready before SSH works.
- **Config file loading applies struct defaults** (`structutils.Defaults` in
  `internal/config/loader_reader.go`) — the chart only renders what it overrides.
- **`default` is a reserved template name** — it's the catch-all in the config server.
- **Session pods vs. ContainerSSH pod**: the chart ships security defaults for session pods
  (`runAsNonRoot`, `runAsUser: 1000`, `allowPrivilegeEscalation: false`, cpu/mem limits). The
  NetworkPolicy deliberately has **no pod-security `enforce` label** — a restricted PSS profile
  would block default pods (they lack `seccompProfile: RuntimeDefault`).
- **containerssh image quirks**: `containerssh/containerssh:v0.6` is Alpine/busybox-based but its
  busybox has **no `openssl`** and no `ssh-keygen` (verified) — host keys can't be generated
  inside that image; it auto-generates an *ephemeral* key at startup if none is configured (writes
  config → fails gracefully read-only).
- **No public config-server image** — `config-server/` must be built and pushed before
  `configServer.enabled=true` will work.

## Commands — build & validate

```bash
# chart
helm lint charts/containerssh
helm template smoke charts/containerssh -n containerssh            # render
helm package charts/containerssh -d /tmp/sshtest

# config server
(cd config-server && go build ./... && go vet ./...)
docker build -t config-server:dev config-server/                  # image

# strongest config validation: feed the rendered config.yaml to the real binary
# (the token/ca.crt files must exist or create fake ones):
docker run --rm \
  -v /tmp/rendered-config.yaml:/etc/containerssh/config.yaml:ro \
  -v <fake-token>:/var/run/secrets/kubernetes.io/serviceaccount/token:ro \
  -v <fake-ca>:/var/run/secrets/kubernetes.io/serviceaccount/ca.crt:ro \
  containerssh/containerssh:v0.6 --config /etc/containerssh/config.yaml --dump-config
```

The rendered config is validated this way after every template change that alters `config.yaml`
(it must exit 0 / dump without `CORE_CONFIG_ERROR`).

## Deployment status

- Target cluster context: **`container-ssh`** (created; control plane reachable).
  Current default context is `ai-platform` — pass `--kube-context container-ssh` explicitly
  (helm) / `--context container-ssh` (kubectl).
- **Nothing deployed yet.** The chart is deploy-ready with `ingress.enabled=false` (ClusterIP +
  port-forward); the Traefik `IngressRouteTCP` support exists but is off for now.
- **Config-server image built & smoke-tested locally** (`config-server:dev`, Docker; verified
  `ubuntu@…` → `ubuntu:22.04` template, unknown user → base). CI to publish it to
  `ghcr.io/tu-wien-datalab/dev.box/config-server` is in `.github/workflows/config-server-image.yml`
  (runs on push to `main`; needs the GHCR package pullable — public, or an `imagePullSecrets` entry).
  `configServer.image.repository` already defaults to that GHCR path.

Remaining before `helm install`:
  1. Create the GitHub repo + push (the config-server image CI runs then).
  2. Choose an auth server and set `auth.password.webhook.url` (config server does **not** auth).
  3. Optional: a stable SSH host key (`ssh.hostKey.existingSecret` vs ephemeral).
  4. Optional, later: `ingress.enabled=true` + the one-time Traefik TCP entrypoint/port setup
     (see values.yaml `ingress`, NOTES.txt).

Typical install command (no ingress):
  ```bash
  helm install -f values.yaml containerssh charts/containerssh \
    --kube-context container-ssh --namespace containerssh --create-namespace
  ```
  then `kubectl --context container-ssh port-forward -n containerssh svc/containerssh 2222:2222`
  and `ssh -p 2222 ubuntu@localhost`.
