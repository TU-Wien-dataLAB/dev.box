# dev.box — AGENTS.md

Working directory for a **ContainerSSH + Kubernetes** dev box. This file gives agents and
maintainers the context needed to work here safely.

## What this is

A personal dev sandbox for running [ContainerSSH](https://containerssh.io) with the **Kubernetes
backend**: users SSH in and are dropped into ephemeral Kubernetes pods. It consists of:

1. **`charts/containerssh/`** — a Helm chart (v2, `containerssh-0.1.1`, `appVersion: 0.6`) that
   deploys ContainerSSH itself plus optional extras.
2. **`config-server/`** — a small Go server implementing the ContainerSSH config webhook protocol
   (built on `go.containerssh.io/containerssh` `config/webhook`), serving **pod templates selected
   by SSH username**.
3. **`auth-server/`** — a small Go server implementing the ContainerSSH **auth** webhook protocol
   (`auth/webhook`) that answers "given an SSH public key, which [authentik](https://goauthentik.io)
   user owns it?" by fingerprint lookup (spec in `auth-server/spec.md`). Optional background sync
   keeps user SSH keys normalized + fingerprinted.

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
│   ├── Chart.yaml             (name containerssh, v0.1.1, appVersion 0.6)
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
│       ├── authserver.yaml    (bundled auth server: Secret + Deployment + Service; auto-wires auth.*.webhook.url)
│       ├── NOTES.txt
│       └── tests/test-connection.yaml
├── config-server/             ← the config webhook server source
│   ├── main.go
│   ├── Dockerfile
│   ├── go.mod                 (go 1.25.3, requires go.containerssh.io/containerssh v0.6.0)
│   └── README.md
├── auth-server/               ← the authentik-backed auth webhook server source
│   ├── main.go                (env/config, wiring, service lifecycle)
│   ├── auth_handler.go        (OnPassword/OnPubKey/OnAuthorization + /config endpoint)
│   ├── authentik.go           (authentik users API client: lookup, group check, pagination)
│   ├── sync.go                (background key-normalize + fingerprint sync, spec §5.3)
│   ├── auth_server_test.go    (tests against an in-memory authentik mock)
│   ├── Dockerfile
│   ├── go.mod                 (go 1.25.3, requires go.containerssh.io/containerssh v0.6.0 + x/crypto)
│   └── README.md
└── tests/
    ├── chart-auth-rendering.sh     (Helm auth fail-fast/render matrix)
    ├── cluster-deployment-spec.md  (staged real-cluster acceptance plan)
    └── spec.md                     (proposed image/wire contract framework)
```

## How it fits together

```
ssh ubuntu@dev.box.example.com
        │ 1. auth: SSH key presented
        ▼
   ContainerSSH (Deployment, port 2222)
        │ 1b. POST /pubkey (username, key) → auth-server
        │     (fingerprint key → GET authentik users by attributes.ssh_key_fingerprint)
        ▼
   auth-server (bundled, auto-wired auth.pubkey.webhook.url)  ⇄  authentik API
        │ success → authenticated as key owner
        ▼ 2. POST /config (username, ip, connectionId)
   config-server (bundled, auto-wired configserver.url)
        │ 3. reply: ONE pod template, merged over base config
        ▼
   session pod in namespace `containerssh-sessions`
        (subnet: networkpolicy = no ingress, egress to internet only)
```

- Per SSH **username** → a pod **template** with that name: `ssh ubuntu@…` → `ubuntu.yaml`.
- Template lookup: `<username>.yaml` → `default.yaml` → (server empty → base pod).
- The config-server response is **merged over the chart's base config** (see merge rule below).
- SSH **key auth** is the auth-server's job: it canonicalizes + fingerprints the presented key and
  queries authentik for the owning user (exact match on `attributes.ssh_key_fingerprint`). Nowhere in
  ContainerSSH, this chart, or the auth server is a password verified against authentik — password
  auth is off by default (test allowlist only).

## Chart key values (`charts/containerssh/values.yaml`)

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository/tag` | `containerssh/containerssh`, `v0.6` | SSH server image |
| `ssh.port` / `service.*` | `2222` / ClusterIP | SSH listener + exposure (NodePort/LB available) |
| `ingress.enabled` / `ingress.tcp.*` | `false` / `ssh` | Traefik IngressRouteTCP (raw TCP) fronts the SSH port; **no cert-manager/TLS** — SSH is not HTTP |
| `ssh.hostKey.existingSecret` / `.privateKey` | `""` | stable host key; else ephemeral key fallback |
| `auth.*.webhook.url` | `""` | external password/pubkey/authz webhook URLs; chart rendering requires password or pubkey unless **auto-wired to the bundled auth-server** |
| `authServer.enabled` | `false` | deploy the bundled authentik-backed auth server + auto-wire `auth.password/pubkey/authz.webhook.url` |
| `authServer.authentik.url` / `.token` | `""` | authentik base URL + service token (or `tokenSecret` existing Secret) — **required** when enabled |
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
- **At least one authentication method is required by the chart**: authz is post-auth and does not
  count. ContainerSSH v0.6 can start with an omitted `auth` block via legacy defaults but has no
  usable webhook authenticator; the chart therefore fails rendering unless `authServer.enabled` or
  a password/public-key webhook URL is set, and never renders `method: webhook` with an empty URL.
- **Auth server is also fail-closed**: with an `auth.*.webhook.url` set, a non-200/error from the
  auth request denies the connection (ContainerSSH retries until the method's `authTimeout`).
  authentik must be reachable from the auth-server pod.
- **Key-first, password-by-test-only**: the bundled auth server never verifies a password against
  authentik; `authServer.passwordUsers` grants an UNVERIFIED password login (test/break-glass only).
  Fingerprints must be unique across users — the sync flags (and refuses to write) duplicates.
- **The bundled auth server is read-only** against authentik except for the optional
  `authServer.syncInterval` job which PATCHes canonical key + fingerprint back (needs edit token).
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
helm lint charts/containerssh --set auth.pubkey.webhook.url=https://auth.example.test
helm template smoke charts/containerssh -n containerssh \
  --set auth.pubkey.webhook.url=https://auth.example.test            # render
tests/chart-auth-rendering.sh                                        # auth render matrix
helm package charts/containerssh -d /tmp/sshtest

# config server
(cd config-server && go build ./... && go vet ./... && go test ./...)
docker build -t config-server:dev config-server/                  # image

# auth server
(cd auth-server && go build ./... && go vet ./... && go test ./...)
docker build -t auth-server:dev auth-server/                      # image

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
- A failed smoke-test release exists in namespace `containerssh`: Helm revision 2 is
  `pending-upgrade` after an aborted upgrade. The config-server and ContainerSSH pods are currently
  Ready, but the mounted config is the omission-only auth draft and has no usable authenticator.
  Treat this as failed test state; uninstall it only as the first step of
  `tests/cluster-deployment-spec.md`.
  Ingress remains disabled (ClusterIP + port-forward).
- **Config-server image built & smoke-tested locally** (`config-server:dev`, Docker; verified
  `ubuntu@…` → `ubuntu:22.04` template, unknown user → base). CI to publish it to
  `ghcr.io/tu-wien-datalab/dev.box/config-server` is in `.github/workflows/config-server-image.yml`
  (runs on push to `main`; needs the GHCR package pullable — public, or an `imagePullSecrets` entry).
  `configServer.image.repository` already defaults to that GHCR path.
- **Auth-server built & unit-tested locally** (`go build/vet/test` green, incl. race). CI to publish
  it to `ghcr.io/tu-wien-datalab/dev.box/auth-server` is in `.github/workflows/auth-server-image.yml`.
  `authServer.image.repository` already defaults to that GHCR path. Not image-built/smoke-tested with
  Docker yet (Docker daemon was off during implementation).

Remaining before the staged cluster validation:
  1. Enroll a dedicated test user/key in production authentik and ensure its fingerprint attribute is
     present; create a read-only service token in an existing Kubernetes Secret.
  2. Create or choose a stable SSH host-key Secret.
  3. Uninstall the current `pending-upgrade` release according to
     `tests/cluster-deployment-spec.md`, then clean-install the bundled auth/config servers.
  4. Optional, later: `ingress.enabled=true` + the one-time Traefik TCP entrypoint/port setup
     (see values.yaml `ingress`, NOTES.txt).

Typical install command (no ingress):
  ```bash
  helm install containerssh charts/containerssh \
    --kube-context container-ssh --namespace containerssh --create-namespace \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.com \
    --set authServer.authentik.tokenSecret=containerssh-authentik-token
  ```
  then `kubectl --context container-ssh port-forward -n containerssh svc/containerssh 2222:2222`
  and `ssh -p 2222 ubuntu@localhost`.
