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
        │   (persistent: deterministic per-user metadata.name + dev.box/owner
        │    label injected, box cap up to configServer.maxPodsPerUser)
        ▼
   persistent user pod in namespace `containerssh-sessions`
        (reused across connections; networkpolicy = no ingress, egress to internet only)
```

- Per SSH **username** → a pod **template** with that name: `ssh ubuntu@…` → `ubuntu.yaml`.
- Template lookup: `<username>.yaml` → `default.yaml` → (server empty → base pod). Unknown
  usernames collapse onto one shared `default` box per owner.
- The config-server response is **merged over the chart's base config** (see merge rule below).
- The lifecycle is ONE stable, deterministic pod per (authenticated user, template): the config
  server injects `kubernetes.pod.metadata.name` = `box-` + first 10 hex of SHA-256 over
  `authenticatedUsername` + resolved template, labels it `dev.box/owner`, and sets
  `createMissingPods: true` in the per-request response. With base `mode: persistent` (normally
  also rendering `createMissingPods: true`), ContainerSSH creates that pod if absent, execs each
  SSH channel in it, and deliberately leaves it running after disconnect.
  The per-owner box cap (`configServer.maxPodsPerUser`, default 3) is enforced server-side in the
  config webhook; reconnects always pass, and listing failures deny (fail closed). Empty
  authenticated identities are denied.
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
| `authServer.enabled` | `false` | deploy the bundled authentik-backed auth server + auto-wire `auth.publicKey/authz.webhook.url` |
| `authServer.authentik.url` / `.token` | `""` | authentik base URL + read-only service token (or `tokenSecret` existing Secret) — **required** when enabled |
| `authServer.authentik.keyAttribute` | `""` | user attribute the presented key is looked up by (default `sshPublicKey`) |
| `authServer.requireGroup` | `""` | optional authentik group required after authentication (authz gate) |
| `kubernetes.sessionNamespace` | `containerssh-sessions` | where user pods run (chart force-manages) |
| `kubernetes.mode` | `persistent` | chart **default**; `connection`/`session` also supported. Persistent normally renders `createMissingPods: true`, never force-renders `generateName`, and requires a bundled/external config server. With mixed-mode chart templates, base `createMissingPods` is omitted and injected only into persistent responses |
| `kubernetes.pod` | security hard defaults | base/fallback pod config |
| `kubernetes.podTemplates` | `[]` | named pod templates (name = SSH username); needs `configServer.enabled` |
| `configServer.enabled` | `false` | deploy the bundled config server + auto-wire `configserver.url` |
| `configServer.maxPodsPerUser` | `3` | per-owner live-box cap in persistent mode (`0` = disabled); reconnects always pass; list-failure denies |
| `configserver.*` | url `""` | client-side config-server connection (timeout/TLS/mTLS) |
| `networkPolicy.enabled` | `true` | backend user pods: deny ingress, egress internet-only (+ kube-dns) |
| `rbac.enabled` | `true` | Role/RoleBinding for pod management in session namespace; the config server's own list-pods Role/RoleBinding render only when `configServer.enabled` |
| `log.level` | `5` | syslog-numbered (7 debug … 2 crit) |

## Non-negotiable invariants / gotchas

- **Log levels are syslog-numbered, higher = more verbose**: `7` debug, `6` info, `5` notice
  (default), `4` warning, `3` error, `2` crit. Do **not** write "0=trace…5=crit" anywhere.
- **Persistent mode is implemented and is the chart default.** ContainerSSH v0.6 persistent mode
  finds a pod by exact `kubernetes.pod.metadata.name`; a `generateName` is not enough; on-demand
  creation needs `kubernetes.pod.createMissingPods: true`. The chart normally renders base
  `mode: persistent` + `createMissingPods: true`; the config server also injects
  `createMissingPods: true`, a deterministic DNS-1123 pod name (`box-` + first 10 hex of SHA-256
  over `authenticatedUsername` + resolved template), and the `dev.box/owner` label per persistent
  request. If a chart-managed template explicitly selects `connection`/`session`, the chart omits
  base `createMissingPods`: mergo skips false source values, so a base true would survive the mode
  override and fail upstream validation. Non-persistent responses skip persistent-only injection. The per-owner cap (`configServer.maxPodsPerUser`, default 3) is enforced
  server-side in the config webhook: reconnect-to-existing always passes, list-failure and empty
  authenticated identity deny (fail closed). Persistent mode without any config server fails
  chart rendering (story 15). Cluster-level disconnect/reconnect coverage still needs the staged
  acceptance plan.
- **Persistent means pod lifecycle, not durable storage.** The pod survives SSH disconnects, but its
  writable layer does not survive pod deletion, eviction, node loss, or recreation. Any data that
  must survive those events needs a PVC or another durable store. Persistent user pods require an
  explicit lifecycle/cleanup policy; ContainerSSH intentionally skips pod removal in this mode.
  Pod-template changes also do not mutate an already-created persistent pod; recreate or migrate the
  pod explicitly when its image/spec must change.
- **The chart force-manages `kubernetes.pod.metadata.namespace` in `configmap.yaml`** (=
  `sessionNamespace`); it force-renders `metadata.generateName` only for non-persistent modes and
  stops doing so in persistent mode to keep the rendered config honest. In persistent mode the
  stable `metadata.name` is injected at runtime through the config-merge path (empty source fields
  are skipped), so the strict-YAML duplicate-key pitfall does not apply. Don't set
  `metadata.namespace`/`generateName` in the base `kubernetes.pod.metadata` — duplicate keys break
  config loading (strict YAML).
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
- **Generated config changes must restart ContainerSSH.** The main Deployment carries a
  `checksum/config` pod-template annotation derived from the chart-managed ConfigMap. Without it,
  a Helm upgrade can leave the old process running with stale mode/settings (observed during the
  first persistent rollout). An external `existingConfigMap` cannot be checksummed by Helm;
  restart the Deployment manually after changing that object.
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
  `configServer.enabled=true` will work. In persistent mode the bundled server also needs its own
  ServiceAccount with `list` on pods in the session namespace (created by the chart), or every
  connection is denied fail-closed when the cap listing fails.

## Commands — build & validate

```bash
# chart
helm lint charts/containerssh --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://auth.example.test
helm template smoke charts/containerssh -n containerssh \
  --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://auth.example.test            # render
tests/chart-auth-rendering.sh                                        # auth render matrix
tests/chart-service-selector.sh                                      # SSH Service isolation
tests/chart-persistent-rendering.sh                                  # persistent-mode render matrix (mode, cap, RBAC)
tests/chart-config-binary-validation.sh                              # rendered configs through real v0.6 binary (Docker)
helm package charts/containerssh -d /tmp/sshtest

# config server
(cd config-server && go build ./... && go vet ./... && go test ./...)
docker build -t config-server:dev config-server/                  # image

# auth server
(cd auth-server && go build ./... && go vet ./... && go test ./...)
docker build -t auth-server:dev auth-server/                      # image

```

`tests/chart-config-binary-validation.sh` feeds rendered connection/persistent bases plus
representative persistent and non-persistent webhook merges to the real
`containerssh/containerssh:v0.6 --dump-config`; every case must exit 0 without
`CORE_CONFIG_ERROR`. Set `CONTAINERSSH_IMAGE` to validate another image.

## Deployment status

- Target cluster context: **`container-ssh`** (created; control plane reachable).
  Current default context is `ai-platform` — pass `--kube-context container-ssh` explicitly
  (helm) / `--context container-ssh` (kubectl).
- Helm release `containerssh` revision 16 is **deployed** in namespace `containerssh` with chart
  `0.2.1` in persistent mode; ContainerSSH, auth-server, and config-server are all Ready. Ingress
  remains disabled (ClusterIP + local port-forward). Chart 0.2.1 adds the generated-config checksum
  that automatically restarted ContainerSSH for this rollout.
- Both bundled servers are pinned to immutable tag `sha-dfa829e`. The authentik read token and
  stable host key are mounted from existing Secrets (`containerssh-authentik-token`,
  `containerssh-host-key`).
- A real persistent-mode reconnect check passed on 2026-09-18: two sequential
  `ssh -i ~/.ssh/slurm_tu_wien -o IdentitiesOnly=yes -p 2222 ubuntu@localhost 'printf READY'`
  connections crossed ContainerSSH → auth-server → production authentik → config-server → guest
  pod and reused the same Running pod (`box-8bbfc143fa`, owner label `matthias.matt`). The pod
  remained after both disconnects, as required.
- The live `ubuntu` template is metadata-only and therefore retains
  `containerssh/containerssh-guest-image`. Authentication uses one exact
  `attributes.sshPublicKey` list query with username enforcement disabled. The enrolled value is a
  single-element list containing the canonical comment-free key. Live measurements after the
  exact-string rollout: enrolled key lookup 199 ms; unknown key denial 75 ms; no parsing,
  fingerprint cache, alternate query, or directory scan. The bundled chart configures `publicKey`
  and `authz` (group gate off by default); password authentication is absent, and the auth server
  no longer serves `/config`.

Remaining before the staged persistent-mode validation:
  1. ✅ (implemented in this change) — persistent-mode contract: the chart renders
     `mode: persistent` + `createMissingPods: true` by default, stops force-rendering
     `generateName`, and fails rendering when persistent mode has no config server. In mixed-mode
     chart templates, base `createMissingPods` is omitted and injected only for persistent
     responses. The config server injects a stable, collision-resistant DNS-1123 `metadata.name`
     from the
     canonical authenticated user (`authenticatedUsername`) + resolved template, labels boxes
     `dev.box/owner`, and enforces the per-owner cap (`configServer.maxPodsPerUser`, default 3;
     reconnect-exempt, fail-closed on listing errors and empty identities). Deletion/retention is
     explicit: no auto-deletion anywhere, documented in the chart README/NOTES. The real-cluster
     disconnect/reconnect stage passed on 2026-09-18.
  2. ✅ Both bundled images are pinned to immutable `sha-dfa829e` tags.
  3. Run the remaining cap-boundary, negative, policy, and fail-closed stages in
     `tests/cluster-deployment-spec.md`.
  4. Optional, later: `ingress.enabled=true` + the one-time Traefik TCP entrypoint/port setup
     (see values.yaml `ingress`, NOTES.txt).

Current auth/config smoke-install command (no ingress; the chart's default is now
`persistent`, so the bundled `configServer` must be enabled — this is the real target shape):
  ```bash
  helm install containerssh charts/containerssh \
    --kube-context container-ssh --namespace containerssh --create-namespace \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.com \
    --set authServer.authentik.tokenSecret=containerssh-authentik-token \
    --set configServer.enabled=true
  ```
  then `kubectl --context container-ssh port-forward -n containerssh svc/containerssh 2222:2222`
  and `ssh -p 2222 ubuntu@localhost`.
