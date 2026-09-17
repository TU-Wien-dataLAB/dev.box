# dev.box — AGENTS.md

Working directory for a **ContainerSSH + Kubernetes** dev box. This file gives agents and
maintainers the context needed to work here safely.

## What this is

A personal dev sandbox for running [ContainerSSH](https://containerssh.io) with the **Kubernetes
backend**. The deployment target is ContainerSSH's **persistent** execution mode: the first SSH
connection creates a stable per-user pod, later connections exec into that same pod, and
disconnecting does not delete it. It consists of:

1. **`charts/containerssh/`** — a Helm chart (v2, `containerssh-0.1.9`, `appVersion: 0.6`) that
   deploys ContainerSSH itself plus optional extras.
2. **`config-server/`** — a small Go server implementing the ContainerSSH config webhook protocol
   (built on `go.containerssh.io/containerssh` `config/webhook`), serving **pod templates selected
   by SSH username**.
3. **`auth-server/`** — a small Go server implementing the ContainerSSH **auth** webhook protocol
   (`auth/webhook`) that answers "given an SSH public key, which [authentik](https://goauthentik.io)
   user owns it?" with one exact `attributes.sshPublicKey` query (spec in `auth-server/spec.md`).

Reference docs (both official, version 0.6):
- Installation in Kubernetes: https://containerssh.io/v0.6/getting-started/installation/ (Kubernetes tab)
- Kubernetes backend reference: https://containerssh.io/v0.6/reference/kubernetes/

Source of truth for ContainerSSH internals: `/Users/matthiasmatt/Documents/Work/oss/ContainerSSH`
(this repo is checked out next to dev.box and is frequently used to verify behavior).

> Repo: `git@github.com:TU-Wien-dataLAB/dev.box.git`. No successful deployment exists yet — see
> [Deployment status](#deployment-status).

## Layout

```
dev.box/
├── AGENTS.md                  ← this file
├── charts/containerssh/       ← the Helm chart
│   ├── Chart.yaml             (name containerssh, v0.1.9, appVersion 0.6)
│   ├── values.yaml            (everything is configurable from here)
│   ├── README.md
│   └── templates/
│       ├── _helpers.tpl
│       ├── configmap.yaml     (renders ContainerSSH config.yaml from values)
│       ├── deployment.yaml    (the ssh server pod)
│       ├── ingress-tcp.yaml   (Traefik IngressRouteTCP for SSH — raw TCP, no TLS/cert-manager)
│       ├── service.yaml
│       ├── serviceaccount.yaml
│       ├── rbac.yaml          (Role/RoleBinding for user pods in the user-pod namespace)
│       ├── networkpolicy.yaml (user pods: no ingress, egress internet-only)
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
│   ├── auth_handler.go        (OnPassword/OnPubKey/OnAuthorization; only /pubkey decides)
│   ├── authentik.go           (authentik users API client: exact key lookup)
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
        │     (exact key string → GET by attributes.sshPublicKey)
        ▼
   auth-server (bundled, auto-wired auth.publicKey.webhook.url)  ⇄  authentik API
        │ success → authenticated as key owner
        ▼ 2. POST /config (username, ip, connectionId)
   config-server (bundled, auto-wired configserver.url)
        │ 3. reply: ONE pod config, merged over base config
        │    (target: mode=persistent + stable per-user metadata.name)
        ▼
   persistent user pod in namespace `containerssh-sessions`
        (reused across connections; networkpolicy = no ingress, egress to internet only)
```

- Per SSH **username** → a pod **template** with that name: `ssh ubuntu@…` → `ubuntu.yaml`.
- Template lookup: `<username>.yaml` → `default.yaml` → (server empty → base pod).
- The config-server response is **merged over the chart's base config** (see merge rule below).
- The target lifecycle is one stable pod name per authenticated user. With
  `mode: persistent` and `createMissingPods: true`, ContainerSSH creates that pod if absent, execs
  each SSH channel in it, and deliberately leaves it running after disconnect.
- SSH **key auth** is the auth-server's job: it queries authentik for exactly one owner of the
  public-key string supplied by ContainerSSH via `attributes.sshPublicKey`. Nowhere in
  ContainerSSH, this chart, or the auth server is a password verified against authentik — password
  auth is off by default (test allowlist only).

## Chart key values (`charts/containerssh/values.yaml`)

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository/tag` | `containerssh/containerssh`, `v0.6` | SSH server image |
| `ssh.port` / `service.*` | `2222` / ClusterIP | SSH listener + exposure (NodePort/LB available) |
| `ingress.enabled` / `ingress.tcp.*` | `false` / `ssh` | Traefik IngressRouteTCP (raw TCP) fronts the SSH port; **no cert-manager/TLS** — SSH is not HTTP |
| `ssh.hostKey.existingSecret` / `.privateKey` | `""` | stable host key; else ephemeral key fallback |
| `auth.*.webhook.url` | `""` | external password/publicKey/authz webhook URLs (v0.6 YAML keys: `password`, `publicKey`, `authz` — NOT `pubkey`); chart rendering requires password or publicKey unless **auto-wired to the bundled auth-server** |
| `auth.*.webhook.timeout` | `30s` | per-request timeout; overall auth timeout defaults to 60s |
| `authServer.enabled` | `false` | deploy the bundled authentik-backed auth server + auto-wire `auth.publicKey.webhook.url` |
| `authServer.authentik.url` / `.token` | `""` | authentik base URL + read-only service token (or `tokenSecret` existing Secret) — **required** when enabled |
| `kubernetes.sessionNamespace` | `containerssh-sessions` | where user pods run (chart force-manages) |
| `kubernetes.mode` | `connection` | chart default; **dev.box target is `persistent`**, not yet fully wired |
| `kubernetes.pod` | security hard defaults | base/fallback pod config |
| `kubernetes.podTemplates` | `[]` | named pod templates (name = SSH username); needs `configServer.enabled` |
| `configServer.enabled` | `false` | deploy the bundled config server + auto-wire `configserver.url` |
| `configserver.*` | url `""` | client-side config-server connection (timeout/TLS/mTLS) |
| `networkPolicy.enabled` | `true` | backend user pods: deny ingress, egress internet-only (+ kube-dns) |
| `rbac.enabled` | `true` | Role/RoleBinding for pod management in session namespace |
| `log.level` | `5` | syslog-numbered (7 debug … 2 crit) |

## Non-negotiable invariants / gotchas

- **Log levels are syslog-numbered, higher = more verbose**: `7` debug, `6` info, `5` notice
  (default), `4` warning, `3` error, `2` crit. Do **not** write "0=trace…5=crit" anywhere.
- **Persistent mode is the deployment target, but is not fully implemented by the chart yet.**
  ContainerSSH v0.6 persistent mode finds a pod by exact `kubernetes.pod.metadata.name`; a
  `generateName` is not enough. For on-demand persistent boxes it also needs
  `kubernetes.pod.createMissingPods: true`. The current chart still defaults to `connection`,
  force-renders only `generateName`, does not expose `createMissingPods`, and the config server does
  not inject a deterministic, collision-resistant DNS-1123 pod name from the canonical
  authenticated identity (`authenticatedUsername`). Do not claim the persistent rollout is ready
  until those gaps and reconnect tests are addressed.
- **Persistent means pod lifecycle, not durable storage.** The pod survives SSH disconnects, but its
  writable layer does not survive pod deletion, eviction, node loss, or recreation. Any data that
  must survive those events needs a PVC or another durable store. Persistent user pods require an
  explicit lifecycle/cleanup policy; ContainerSSH intentionally skips pod removal in this mode.
  Pod-template changes also do not mutate an already-created persistent pod; recreate or migrate the
  pod explicitly when its image/spec must change.
- **The chart currently force-manages `kubernetes.pod.metadata.namespace` and `generateName`** in
  `configmap.yaml` (= `sessionNamespace`, `containerssh-`). Don't set them in the base
  `kubernetes.pod.metadata` — duplicate keys break config loading (strict YAML). Persistent support
  must replace/condition this behavior and provide `metadata.name` instead.
- **The config server / override merge**: ContainerSSH merges the webhook response over the base
  config via `structutils.Merge` = `mergo.Merge(dst, src, mergo.WithOverride)`. Empty/nil source
  fields are **skipped**, so partial per-template overrides are safe (proven by a mergo test); a
  template that only sets labels keeps the base pod's containers/spec.
- **Backend images must contain `containerssh-agent`**: the base pod runs
  `/usr/bin/containerssh-agent wait-signal ...`. Replacing only its image with plain
  `ubuntu:22.04` inherits that command and fails with `stat /usr/bin/containerssh-agent: no such
  file or directory`. Keep templates metadata-only unless the custom image implements the guest
  image contract.
- **Bundled webhook URLs need port `:8080`**, because their Services expose 8080 rather than port
  80. The main SSH Service must additionally select `app.kubernetes.io/component: server`; the
  shared name/instance selector alone also selects the auth/config pods and randomly misroutes SSH.
- **Config server is fail-closed**: with `configserver.url` set, ContainerSSH *denies* connections
  when the config POST fails (retries every 10 s, non-200 = "Cannot authenticate at this time").
  Bundled server must be Ready before SSH works.
- **At least one authentication method is required by the chart**: authz is post-auth and does not
  count. ContainerSSH v0.6 can start with an omitted `auth` block via legacy defaults but has no
  usable webhook authenticator; the chart therefore fails rendering unless `authServer.enabled` or
  a password/public-key webhook URL is set, and never renders `method: webhook` with an empty URL.
- **`auth.publicKey`, never `auth.pubkey`, in rendered config.yaml**: ContainerSSH v0.6's
  `AuthConfig` unmarshals `pubkey` as a deprecated `*bool` flag in BOTH its legacy and new YAML
  structs — a map under `pubkey` aborts config loading (`cannot unmarshal !!map into bool`, caught
  by the real-binary `--dump-config` check on 2026-09-15; this is what crash-looped the first
  cluster release). The public-key webhook section must render as `auth.publicKey`; the chart value
  key is also `auth.publicKey`. `authz` and `password` are plain struct keys.
- **Auth server is also fail-closed**: with an `auth.*.webhook.url` set, a non-200/error from the
  auth request denies the connection (ContainerSSH retries until the method's `authTimeout`).
  authentik must be reachable from the auth-server pod. Per-request webhook timeout is 30s.
- **Public-key lookup is strict and constant-cost**: the auth server performs one exact authentik
  filter for `attributes.sshPublicKey` as a single-element JSON list containing the key string from
  ContainerSSH. Exactly one user authenticates; zero or multiple users deny cleanly. Values are not
  parsed or normalized, and there is no directory scan.
- **Public-key only**: the bundled chart configures no password webhook; the protocol-required
  `OnPassword` implementation always denies. The SSH username intentionally selects the pod
  template and is not compared with the authentik owner; authenticated metadata records the owner.
- **The bundled auth server is read-only** against authentik. Its service token needs only user-view
  permission.
- **Config file loading applies struct defaults** (`structutils.Defaults` in
  `internal/config/loader_reader.go`) — the chart only renders what it overrides.
- **`default` is a reserved template name** — it's the catch-all in the config server.
- **User pods vs. ContainerSSH pod**: the chart ships security defaults for backend user pods
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
helm lint charts/containerssh --set auth.publicKey.webhook.url=https://auth.example.test
helm template smoke charts/containerssh -n containerssh \
  --set auth.publicKey.webhook.url=https://auth.example.test            # render
tests/chart-auth-rendering.sh                                        # auth render matrix
tests/chart-service-selector.sh                                      # SSH Service isolation
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
- Helm release `containerssh` revision 12 is **deployed** in namespace `containerssh` with chart
  `0.1.8`; ContainerSSH, auth-server, and config-server are all Ready. Ingress remains disabled
  (ClusterIP + local port-forward).
- The auth-server is pinned to immutable tag `sha-f3dba9d` (image digest
  `sha256:2c34fe133489b3e6183c2745f85a8f46d3f0afd740dfc3d81112fc9370b8dad0`); the config-server
  still uses `main`. The authentik read token and stable host key are mounted from existing Secrets
  (`containerssh-authentik-token`, `containerssh-host-key`).
- A real connection-mode SSH check passed on 2026-09-17:
  `ssh -i ~/.ssh/slurm_tu_wien -o IdentitiesOnly=yes -p 2222 ubuntu@localhost 'printf READY'`.
  It crossed ContainerSSH → auth-server → production authentik → config-server → guest pod and
  returned `READY`; the connection pod was deleted after disconnect as expected.
- The live `ubuntu` template is metadata-only and therefore retains
  `containerssh/containerssh-guest-image`. Authentication uses one exact
  `attributes.sshPublicKey` list query with username enforcement disabled. The enrolled value is a
  single-element list containing the canonical comment-free key. Live measurements after the
  exact-string rollout: enrolled key lookup 199 ms; unknown key denial 75 ms; no parsing,
  fingerprint cache, alternate query, or directory scan. The bundled chart configures `publicKey`
  and `authz` (group gate off by default); password authentication is absent, and the auth server
  no longer serves `/config`.

Remaining before the staged persistent-mode validation:
  1. Implement the persistent-mode contract in the chart/config server: render
     `mode: persistent` and `createMissingPods: true`, derive a stable, collision-resistant
     DNS-1123 `metadata.name` from the canonical authenticated user (`authenticatedUsername`), stop
     relying on `generateName`, define explicit deletion/retention behavior, and add
     disconnect/reconnect coverage.
  2. Pin the config-server image to an immutable SHA tag and run the remaining negative, policy,
     and fail-closed stages in `tests/cluster-deployment-spec.md`.
  3. Optional, later: `ingress.enabled=true` + the one-time Traefik TCP entrypoint/port setup
     (see values.yaml `ingress`, NOTES.txt).

Current auth/config smoke-install command (no ingress; still uses the chart's non-target
`connection` default until item 1 is implemented):
  ```bash
  helm install containerssh charts/containerssh \
    --kube-context container-ssh --namespace containerssh --create-namespace \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.com \
    --set authServer.authentik.tokenSecret=containerssh-authentik-token
  ```
  then `kubectl --context container-ssh port-forward -n containerssh svc/containerssh 2222:2222`
  and `ssh -p 2222 ubuntu@localhost`.
