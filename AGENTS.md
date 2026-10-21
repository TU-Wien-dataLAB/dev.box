# dev.box — AGENTS.md

Working directory for a **ContainerSSH + Kubernetes** dev box. This file gives agents and
maintainers the context needed to work here safely.

## What this is

A personal dev sandbox for running [ContainerSSH](https://containerssh.io) with the **Kubernetes
backend**. The deployment target is ContainerSSH's **persistent** execution mode: the first SSH
connection creates a stable per-user pod, later connections exec into that same pod, and
disconnecting does not delete it. It consists of:

1. **`charts/containerssh/`** — a Helm chart (v2, `containerssh-0.4.2`, `appVersion: 0.6`) with an optional official Kata Containers dependency that
   deploys ContainerSSH itself plus optional extras.
2. **`config-server/`** — a small Go server implementing the ContainerSSH config webhook protocol
   (built on `go.containerssh.io/containerssh` `config/webhook`), serving **pod templates selected
   by SSH username**, plus the code-owned reserved `settings` application.
3. **`auth-server/`** — a small Go server implementing the ContainerSSH **auth** webhook protocol
   (`auth/webhook`) that answers "given an SSH public key, which [authentik](https://goauthentik.io)
   user owns it?" with one exact `attributes.sshPublicKey` query (spec in `auth-server/spec.md`).

Reference docs (both official, version 0.6):
- Installation in Kubernetes: https://containerssh.io/v0.6/getting-started/installation/ (Kubernetes tab)
- Kubernetes backend reference: https://containerssh.io/v0.6/reference/kubernetes/

Source of truth for ContainerSSH internals: our fork
[`TU-Wien-dataLAB/ContainerSSH`](https://github.com/TU-Wien-dataLAB/ContainerSSH).
The reference checkout at `/Users/matthiasmatt/Documents/Work/oss/ContainerSSH`
is for inspecting behavior; source changes use the worktree workflow below.

> Repo: `git@github.com:TU-Wien-dataLAB/dev.box.git`. See
> [Deployment status](#deployment-status) for the live release and verified behavior.

## ContainerSSH fork workflow

- `origin` is **`TU-Wien-dataLAB/ContainerSSH`**; `upstream` is
  `ContainerSSH/ContainerSSH`. ContainerSSH fixes and their PRs belong to our fork,
  not upstream and not this `dev.box` repository.
- **Before editing ContainerSSH source, ask the user to manually create a new
  worktree in the fork using herdr.** Example: “Please create a new ContainerSSH
  fork worktree in herdr and share its absolute path and branch name.” Wait for
  that path, then work there. Use an already-provided worktree; leave the reference
  checkout unchanged and do not create the worktree automatically.
- Commit/push the feature branch from the confirmed worktree. Confirm the PR base
  branch with the user, and explicitly target the fork with
  `gh pr create --repo TU-Wien-dataLAB/ContainerSSH --base <confirmed-base>`;
  do not rely on GitHub CLI's automatic fork/upstream selection.
- Chart/webhook/application changes and deployment evidence stay in `dev.box`.
  Keep them out of the ContainerSSH source PR; live rollout approval is separate.

## Layout

```
dev.box/
├── AGENTS.md                  ← this file
├── charts/containerssh/       ← the Helm chart
│   ├── Chart.yaml             (name containerssh, v0.4.0, appVersion 0.6)
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
│       ├── networkpolicy.yaml (user pods: no ingress, egress internet-only; settings pods: one scoped API exception)
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
├── settings-tui/              ← the settings management TUI (the only program in its image)
│   ├── main.go / model.go / view.go   (management model + rendering)
│   ├── boxes.go               (trusted env parsing, deterministic box naming)
│   ├── kube.go                (per-name pod get/delete behind a small client interface)
│   ├── model_test.go          (offline management logic against a fake client)
│   ├── main_test.go           (executable PTY tests, incl. a stand-in Kubernetes API)
│   ├── Dockerfile
│   ├── go.mod                 (go 1.26.0, Bubble Tea v2, stdlib-only Kubernetes client)
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
- Ordinary lookup: `<username>.yaml` → `<username>.json` → error (connection denied).
  No catch-all or base-pod fallback; `default` is an ordinary template name used only by
  `ssh default@…`. Empty/unsafe usernames are rejected rather than rewritten.
  **Reserved exception:** `settings` is always synthesized in code, without a template file.
- The config-server response is **merged over the chart's base config** (see merge rule below).
- The lifecycle is ONE stable, deterministic pod per (authenticated user, template): the config
  server injects `kubernetes.pod.metadata.name` = `box-` + first 10 hex of SHA-256 over
  `authenticatedUsername` + resolved template, labels it `dev.box/owner`, and sets
  `createMissingPods: true` in the per-request response. With base `mode: persistent` (without
  base `createMissingPods`), ContainerSSH creates that pod if absent, execs each
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
| `image.repository/tag` | digest-pinned fork PR #10 (see values) | Settings fixes plus auth denial banners; overrides require the message patch |
| `ssh.port` / `service.*` | `2222` / ClusterIP | SSH listener + exposure (NodePort/LB available) |
| `ingress.enabled` / `ingress.tcp.*` | `false` / `ssh` | Traefik IngressRouteTCP (raw TCP) fronts the SSH port; **no cert-manager/TLS** — SSH is not HTTP |
| `ssh.hostKey.existingSecret` / `.privateKey` | `""` | stable host key; else ephemeral key fallback |
| `auth.*.webhook.url` | `""` | external password/publicKey/authz webhook URLs (v0.6 YAML keys: `password`, `publicKey`, `authz` — NOT `pubkey`); chart rendering requires password or publicKey unless **auto-wired to the bundled auth-server** |
| `auth.*.webhook.timeout` | `30s` | per-request timeout; overall auth timeout defaults to 60s |
| `authServer.enabled` | `false` | deploy the bundled authentik-backed auth server + auto-wire `auth.publicKey/authz.webhook.url` |
| `authServer.authentik.url` / `.token` | `""` | authentik base URL + read-only service token (or `tokenSecret` existing Secret) — **required** when enabled |
| `authServer.authentik.keyAttribute` | `""` | user attribute the presented key is looked up by (default `sshPublicKey`) |
| `authServer.requireGroup` | `""` | optional authentik group required after authentication (authz gate) |
| `kata.enabled` | `true` | official Kata 4.2.0 subchart; injects `kata-qemu-runtime-rs` into ordinary named templates unless overridden there; base and settings carry no RuntimeClass |
| `kubernetes.sessionNamespace` | `containerssh-sessions` | where user pods run (chart force-manages) |
| `kubernetes.mode` | `persistent` | chart **default**; `connection`/`session` also supported. Persistent requires a config server; `createMissingPods: true` is injected only into persistent responses, never the shared base |
| `kubernetes.pod` | security hard defaults | unconditionally restricted shared base; heavy fields belong in ordinary templates |
| `kubernetes.podTemplates` | `[]` | ordinary named templates; needs `configServer.enabled`; `settings` is reserved, never an item here |
| `settings.image.repository/tag` | see values | only settings pod customization; digest-pin for rollout (management UI image) |
| `configServer.enabled` | `false` | deploy the bundled config server + auto-wire `configserver.url` |
| `configServer.maxPodsPerUser` | `3` | per-owner live-box cap in persistent mode (`0` = disabled); reconnects always pass; list-failure denies |
| `configserver.*` | url `""` | client-side config-server connection (timeout/TLS/mTLS) |
| `networkPolicy.enabled` | `true` | backend user pods: deny ingress, egress internet-only (+ kube-dns) |
| `networkPolicy.settingsApiEgress` | `true` | settings-only egress exception to the Kubernetes API (TCP 443/6443 to kube-system `kube-apiserver` pods by default; override `to`/`ports` per cluster) |
| `rbac.enabled` | `true` | Role/RoleBinding for pod management in session namespace; config-server reconciliation permissions render when `configServer.enabled` (see scoped-owner RBAC below) |
| `log.level` | `5` | syslog-numbered (7 debug … 2 crit) |

## Non-negotiable invariants / gotchas

- **Log levels are syslog-numbered, higher = more verbose**: `7` debug, `6` info, `5` notice
  (default), `4` warning, `3` error, `2` crit. Do **not** write "0=trace…5=crit" anywhere.
- **Persistent mode is implemented and is the chart default.** ContainerSSH v0.6 persistent mode
  finds a pod by exact `kubernetes.pod.metadata.name`; a `generateName` is not enough; on-demand
  creation needs `kubernetes.pod.createMissingPods: true`. The chart always renders
  base `mode: persistent` without base `createMissingPods`; bundled/external config servers inject
  `createMissingPods: true`, a deterministic DNS-1123 pod name (`box-` + first 10 hex of SHA-256
  over trimmed `authenticatedUsername` + NUL + resolved template), and the `dev.box/owner` label
  per persistent request. Mergo skips false source values: a base true would survive the settings
  connection-mode override and fail validation. External bases serving settings need the same audit. Non-persistent responses skip this injection. The per-owner cap (`configServer.maxPodsPerUser`, default 3) is enforced
  server-side in the config webhook: reconnect-to-existing always passes, list-failure and empty
  authenticated identity deny (fail closed). Persistent mode without any config server fails
  chart rendering. The historical cluster reconnect check passed; new regression coverage follows
  the staged acceptance plan.
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
- **Settings / merge boundary:** `settings` is always code-owned, connection-mode, one session
  per transport, direct `/usr/local/bin/dev-box-settings` (no shell wrapper), PTY/application-shell
  only; exec/subsystem/env/signal/forwarding denied. The TUI is the **management UI** (master–detail,
  validated as HTML prototypes on a throwaway branch): master rows carry only the template
  name (left) and the state word (right); the selected box's detail pane carries a status strip
  (status, uptime, restarts) over a ledger (image, runtime, requests, limits); the master pane is
  footed by the per-owner cap line `created N/max`; both panes fill the body budget — blank rows
  grow above each pane's footed block, so the boxes close on one line and the footer sits at the
  terminal bottom; ~3 s polling; and exactly one action — delete
  behind a centered `y`-confirmation popup naming all three consequences. Owner env is the
  raw server-verified identity (blank denied); display sanitization never changes authorization.
  `DEV_BOX_BOX_TEMPLATES` carries the owner's ordinary persistent template names from the same walk
  that computes the Role's `resourceNames` (empty list valid; unreadable templates deny).
  `DEV_BOX_MAX_BOXES` carries the operator's enforced cap (config-server injects its
  `CONTAINERSSH_MAX_PODS_PER_USER`; 0/unset hides the element); the UI's count is advisory — it is
  derived from the owner's own rows (Terminating counts, not-created does not; no list permission
  exists) while the config server's list-based check stays authoritative. The TUI
  recomputes each box's deterministic name with the persistent hashing contract (trimmed owner +
  NUL + template, pinned by golden tests) and reads/deletes pods only through the owner's projected
  token. `DEV_BOX_KUBE_API_URL` is the offline-validation override for the Docker smoke; the config
  server never sets it. Unreachable API ⇒ rows dim, delete disabled, never a crash.
  The shared base spec unconditionally permits only `containers`, `securityContext`,
  `restartPolicy`, `terminationGracePeriodSeconds`. Base pod securityContext permits only
  `runAsNonRoot`, `runAsUser`, `runAsGroup`, `seccompProfile.type`; base metadata permits only
  labels/annotations without `dev.box/owner`. Move runtime classes, volumes, service accounts and
  other heavy fields into ordinary templates. Empty/false
  webhook fields cannot clear unsafe inherited values. Ordinary templates get Kata injection;
  settings runs on the default runtime.
- **Scoped owner RBAC:** settings reconciles one ServiceAccount, Role and RoleBinding per owner,
  all named `dev-box-owner-` + first **48** hex of SHA-256 over the trimmed verified owner.
  The Role grants only pod `get/delete` on exact deterministic persistent names recomputed from
  mounted templates; settings/non-persistent names are excluded. Zero names means **zero rules**,
  never an empty `resourceNames` grant (which would allow every pod). Errors deny the request.
  ServiceAccount automount defaults **false**; only the settings pod explicitly sets it **true**,
  mounting its scoped projected token at `/var/run/secrets/kubernetes.io/serviceaccount`.
  Config-server requires pods `list/get/delete` and serviceaccounts/roles/rolebindings
  `get/list/create/update` in the session namespace, without `bind/escalate`. It must itself hold
  delegated pod `get/delete` for Kubernetes anti-escalation checks; updates are needed to reconcile.
  This is a trust upgrade from list-only access. Three objects persist per owner; GC is explicit
  operator work only. NetworkPolicy adds **exactly one scoped exception**
  (`networkPolicy.settingsApiEgress`): a second NetworkPolicy allowing settings pods
  (`dev.box/application=settings`) egress to the Kubernetes API only (default peers: kube-system
  `kube-apiserver` pods, TCP 443+6443 — Calico matches the post-NAT pod port); ordinary pods keep
  deny-ingress/egress-internet-only. API authorization via operator impersonation is still not
  proof of network reachability or token use.
  **Before settings migration, RBAC changes, or acceptance**, read
  [the migration and staged plan](tests/settings-code-migration.md) and for the management UI
  [the acceptance plan](tests/settings-management-migration.md).
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
- **Known rough edge: disabled SSH methods are still advertised.** Stock ContainerSSH v0.6's
  `internal/sshserver/serverImpl.go` unconditionally registers password and keyboard-interactive
  callbacks. OpenSSH may therefore prompt for a password after a key/template denial, although
  the bundled setup never accepts passwords. This is intentionally retained: no Helm/auth-webhook
  setting fixes advertisement; changing it requires a ContainerSSH patch and custom image.
  Client workaround: `PreferredAuthentications=publickey` + `IdentitiesOnly=yes`. `BatchMode=yes`
  additionally suppresses interactive prompts (including local encrypted-key passphrases).
- **The bundled auth server is read-only** against authentik. Its service token needs only user-view
  permission.
- **Config file loading applies struct defaults** (`structutils.Defaults` in
  `internal/config/loader_reader.go`) — the chart only renders what it overrides.
- **Generated config changes must restart ContainerSSH.** The main Deployment carries a
  `checksum/config` pod-template annotation derived from the chart-managed ConfigMap. Without it,
  a Helm upgrade can leave the old process running with stale mode/settings (observed during the
  first persistent rollout). An external `existingConfigMap` cannot be checksummed by Helm;
  restart the Deployment manually after changing that object.
- **Ordinary named templates are required by the config server** — unmatched SSH usernames return
  an error in every mode (issue #6); the sole exception is code-synthesized `settings` with no file.
  There is no `default` or base-config fallback. `default` is an ordinary template name.
  ContainerSSH retries non-200 config responses every ten seconds until timeout, then silently
  closes SSH. With both bundled servers enabled, the chart passes ordinary names **plus `settings`**
  in `AUTH_SERVER_ALLOWED_TEMPLATES`; unknown names get HTTP 200 + `success: false` for immediate
  SSH `Permission denied`. With zero ordinary templates only settings remains available. In the
  auth-server protocol itself an empty allowlist denies all, unset disables the gate, and malformed
  policy fails startup.
  The optional authentik group gate still applies. External authz webhooks need their own early
  template gate; config-server rejection remains the safety net.
- **User pods vs. ContainerSSH pod**: the chart ships security defaults for backend user pods
  (`runAsNonRoot`, `runAsUser: 1000`, `allowPrivilegeEscalation: false`, cpu/mem limits). The
  NetworkPolicy deliberately has **no pod-security `enforce` label** — a restricted PSS profile
  would block default pods (they lack `seccompProfile: RuntimeDefault`).
- **containerssh image quirks**: `containerssh/containerssh:v0.6` is Alpine/busybox-based but its
  busybox has **no `openssl`** and no `ssh-keygen` (verified) — host keys can't be generated
  inside that image; it auto-generates an *ephemeral* key at startup if none is configured (writes
  config → fails gracefully read-only).
- **Config-server startup requires Kubernetes in all modes:** `main` requires
  `CONTAINERSSH_SETTINGS_IMAGE` and an in-cluster client even for connection/session operation.
  Use offline handler tests for local development, or an approved deployed service/port-forward;
  ordinary HTTP handler tests can use fixtures without a real API. Build/publish an immutable
  config-server image containing the new contracts before rollout. The chart supplies its dedicated
  ServiceAccount and reconciliation permissions; API errors fail closed.

## Commands — build & validate

```bash
# chart
helm dependency build charts/containerssh                           # pinned optional Kata dependency
helm lint charts/containerssh --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://auth.example.test
helm template smoke charts/containerssh -n containerssh \
  --set kubernetes.mode=connection \
  --set auth.publicKey.webhook.url=https://auth.example.test            # render
tests/chart-auth-rendering.sh                                        # auth render matrix
tests/chart-template-authorization.sh                                # template allowlist wiring
tests/chart-service-selector.sh                                      # SSH Service isolation
tests/chart-kata-rendering.sh                                         # Kata installer toggle + backend RuntimeClass
tests/chart-persistent-rendering.sh                                  # persistent-mode render matrix (mode, cap, RBAC)
tests/chart-config-binary-validation.sh                              # rendered configs through real v0.6 binary (Docker)
helm package charts/containerssh -d /tmp/sshtest

# config server
(cd config-server && go build ./... && go vet ./... && go test ./...)
docker build -t config-server:dev config-server/                  # image

# auth server
(cd auth-server && go build ./... && go vet ./... && go test ./...)
docker build -t auth-server:dev auth-server/                      # image

# Live regression (local SSH port-forward + enrolled key + known ubuntu template):
python3 tests/ssh-template-rejection.py --key ~/.ssh/slurm_tu_wien  # deny within 5s

# Error-message delivery through the real gateway image (Docker only, no cluster):
tests/gateway-error-messages.sh                                     # denial banners via stub auth webhook
#   CONTAINERSSH_IMAGE=<ref> tests/gateway-error-messages.sh        # any other image

```

`tests/chart-config-binary-validation.sh` feeds rendered bases and actual synthesized settings /
representative ordinary webhook merges through the real merger and pod/security validators, then
`--dump-config` on the fork gateway (`ghcr.io/tu-wien-datalab/containerssh:deploy` by default).
Every valid case must exit 0 without `CORE_CONFIG_ERROR`; parsing alone is not policy acceptance.
Set `CONTAINERSSH_IMAGE` to validate the intended immutable rollout image.

## Deployment status

**Current status (2026-10-21, latest):** revision **46**, chart **0.4.2**. Issue #7's settings TUI
runs the validated **master–detail layout** (HTML prototypes on branch
`prototype/settings-tui-c4`; captured answer on the issue) with **full-height equal panes**.
The Settings guest runs
`settings-tui:sha-f0b776cc6e36ee44f3db4e8a3dfe0bebf2e2f9ec@sha256:371f2cb1…` (from commit
`f0b776c`; panes fill the body budget — blank rows grow above each pane's footed block, boxes
close on one line, footer at the terminal bottom; honesty line now needs budget ≥ strip+ledger+4,
fixing a 10-row overflow), config-server runs
`sha-5ee5ec1@sha256:c1f051ee…` (both from rollout commit `5ee5ec1`; this push triggered both image
workflows and they were additionally dispatched, racing two builds per tag — use one or the other;
config-server tags are 7-char short SHAs, settings tags are full SHAs); gateway and auth-server
pins are unchanged. Chart defaults pin the new settings image; chart 0.4.1. The TUI shows the
master pane (name + state only, `created N/max` cap line from `DEV_BOX_MAX_BOXES` injected by the
new config-server), the detail strip/ledger (STATUS/UPTIME/RESTARTS over IMAGE/RUNTIME/REQUESTS/
LIMITS), and the centered delete popup; the footer carries keys + verified owner + connection and
**clips the owner below ~80 cols** (probe predicates are width-aware). Live gates all passed:
master–detail screen at 100×24, popup open/cancel (nothing deleted; delete+recreate stays
live-accepted from revision 44 — full management flow exercised against the new image in the
Docker smoke), policy suite 82 PASS, lifecycle suite PASS, unknown-template rejection 0.42 s,
ordinary box UID `cd0c5a5a-…` unchanged. **Read [live evidence and caveats]
(tests/settings-c4-2026-10-21.md)**: the TUI cap count is advisory (its Role grants no pod list;
the config server's check is authoritative), differential-renderer splits cap-line fragments in
tests, #10 (two real owners) gates pre-alpha, #12 (PVC) and #13 (metrics) remain open.
**Revision 45 rendered content-height panes; revision 46 is the full-height equal-pane rollout
(f0b776c): image published by the push-triggered workflow (no dispatch — avoid racing two builds
per tag), Docker smoke all-pass against the published image, live gates green (100×24 screen peek
with both boxes closing on one line and the footer on the last row, popup unchanged, policy suite
82 PASS with the new image observed in-pod, lifecycle suite PASS, unknown-template rejection
0.51 s, ordinary box UID `cd0c5a5a-…` unchanged).**

**Historical revision-45 baseline (2026-10-21, earlier):** revision **45**, chart **0.4.1**. The
`f0b776c` predecessor `5ee5ec1` deployed the master–detail layout with content-height panes:
settings-tui `sha-5ee5ec1c283…@sha256:219f07e4…`, config-server `sha-5ee5ec1@sha256:c1f051ee…`
(both from rollout commit `5ee5ec1`; that push triggered both image workflows and they were
additionally dispatched, racing two builds per tag — use one or the other; config-server tags are
7-char short SHAs, settings tags are full SHAs); gateway and auth-server pins unchanged. Live
gates: master–detail screen at 100×24, popup open/cancel (nothing deleted; delete+recreate stayed
live-accepted from revision 44), policy suite 82 PASS, lifecycle suite PASS, unknown-template
rejection 0.42 s, ordinary box UID unchanged. Evidence:
[tests/settings-c4-2026-10-21.md](tests/settings-c4-2026-10-21.md).

**Historical revision-44 baseline (2026-10-21, earlier):** revision **44**, chart **0.4.0**. Issue #7's settings management
UI is **deployed and live-accepted** with user approval. The Settings guest runs
`settings-tui:sha-901d8a7eb8092db87b4fbd44fbed64beb74c1de3@sha256:08de4f9e…`, config-server runs
`sha-901d8a7@sha256:db773cd2…` (both published from rollout commit `901d8a7` via explicit
`workflow_dispatch` — the push-triggered image workflows did not fire for that SHA); gateway and
auth-server pins are unchanged. Chart defaults now pin the management-UI settings image.
Rollout went 42 (images) → 43 (temporary `smoke-mgmt` template) → 44 (template removed); the
owner's real box kept UID `b2abba32-…` throughout. Live gates all passed: UI rows/Live/refresh,
delete dialog with the three consequences, **delete + recreate from the UI** (Terminating →
not-created → same deterministic name, new UID), full SSH policy probe suite (exec/subsystem/env/
signal/forwarding denied; one session per transport; resize stress), lifecycle (SIGKILL client,
control master), unknown-template rejection 0.18 s, impersonated owner authorization (get/delete
on exact names only), scoped settings→API egress exercised by live requests (kubeadm-style
apiserver pods match the default selector; 443+6443 for Calico post-NAT). The egress NetworkPolicy
`containerssh-sessions-settings-api` is the **only** policy change; ordinary pods are unchanged.
**Read [live evidence and caveats](tests/settings-management-2026-10-21.md)** before touching the
rollout: un-drained PTY stalls session close after `q` (driver artifact, real terminals drain),
OpenSSH `-tt` with piped stdout swallows ordinary-box exec output (client artifact), #10
(two real owners) gates pre-alpha, #12 (PVC) and #13 (metrics) remain open. The revision-41
issue-#11 notes below remain the historical record for the Settings migration.

**Historical revision-41 baseline (2026-10-19):** revision **41**, chart **0.3.3**. Issue #11's code-owned Settings
migration is deployed with user approval. Config-server is pinned to
`sha-5bca171@sha256:b74681bf6d3cd12ccfd01f7e4b6f5d368386882b297375d77e030f782dc204dc`;
gateway/auth and the Settings guest retain their previous digest pins. The old Settings template
is removed; default-runtime Settings uses its owner's projected token and exact-name pod Role.
Live policy/lifetime, projected-file presence, impersonated authorization, isolated reconciliation/
failure/cap checks passed; ordinary Kata pod UID is unchanged and fixtures are removed.
For issue-#11 evidence and limitations see
[live evidence and limitations](tests/settings-code-live-validation.md): inherited Calico policy
permissions remain, mounted-bearer authentication was not exercised, API egress stays blocked,
and two-real-owner acceptance remains #10.

**Code default versus rollout:** fork [PR #11](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/11)
merged `settings-change` → `deploy` on 2026-10-16; deploy-line
[Docker Image run 38917607974](https://github.com/TU-Wien-dataLAB/ContainerSSH/actions/runs/38917607974)
succeeded. Chart 0.3.3 defaults to the digest-pinned PR #10 gateway in `values.yaml` for auth
denial banners as well as Settings fixes; PR #10 remains unmerged. Stock v0.6.0 and unpatched
`deploy` strictly reject the auth `message` field, so overridden gateways must include the patch.
This default is not a live image selection. Existing rollout image pins are independent of chart
defaults. Issue #11 migration and acceptance require [this staged plan](tests/settings-code-migration.md).

**Historical revision-40 baseline (2026-10-16):** chart **0.3.2**. The gateway runs the
digest-pinned [fork PR #10](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/10) test image
(`error-messages` → `deploy`),
`ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d`:
PR #11 (Settings fixes, merged into `deploy`) plus user-facing auth denial messages — an HTTP 200
auth-webhook denial with a non-empty `message` becomes a terminal failure delivered to the SSH
client as a userauth banner (RFC 4252 §5.4) right before `Permission denied`. The PR is **not
merged**. The bundled auth-server now sends those messages (branch
`auth-server-error-messages`, image `sha-245b9e2@sha256:36e68dc2…`, digest-pinned since revision
40 — the stock v0.6.0 `auth/webhook` handler cannot serialize a message, so `auth-server/webhook.go`
implements the wire contract locally). Live: `ssh unknown@…` prints
`no pod template matches SSH username 'unknown'` before the denial in ~0.4 s; unenrolled keys print
`public key is not enrolled for any user`; infrastructure failures stay fail-closed HTTP 500 with
no message. Rollout regression passed: `ubuntu` positive control (same box, Kata kernel 6.18.35),
prompt unknown-template rejection, `tests/gateway-error-messages.sh` (stub-webhook image test), and
a local Docker E2E with the real auth-server + gateway images. Revision 40 was upgraded from the
chart at `d0a02e6` because `b040bde` (code-owned Settings) rejects the live `settings` pod template
in `kubernetes.podTemplates` — that value migration completed at revision 41 above. For historical Settings acceptance read
[fixture evidence](tests/settings-fixture-validation.md) (revision 38, all gates passed) and
[non-disruptive evidence](tests/settings-remaining-validation.md); two real enrolled owners and
other explicitly listed acceptance gaps remain; issue #9 is open. The revision-19 notes below
describe the pre-Settings baseline; the [revision-20 report](tests/settings-live-validation.md) is
historical.

- Target cluster context: **`container-ssh`** (created; control plane reachable).
  Current default context is `container-ssh` — still pass `--kube-context container-ssh` explicitly
  (helm) / `--context container-ssh` (kubectl).
- **Historical revision-19 baseline:** Helm release `containerssh` revision 19 was deployed in namespace `containerssh` with chart
  `0.3.1` in persistent mode (upgraded 2026-09-30); ContainerSSH, auth-server, and config-server are
  all Ready. Ingress remains disabled (ClusterIP + local port-forward). Revision 19 added the
  auth-server template authorization gate (`AUTH_SERVER_ALLOWED_TEMPLATES=["ubuntu"]`) and changed
  only `authServer.image.tag` in user values. Secret references, templates, and config-server tag
  were preserved. The main SSH server restarted because chart metadata changed its config checksum.
- Kata 4.2.0 is installed by the chart's `kata` DaemonSet, Ready on both workers (not the tainted
  control-plane nodes). `kata-qemu-runtime-rs` exists, and both workers advertise
  `katacontainers.io/kata-runtime=true`. The generated base config selects it for new user pods.
  When first adding this dependency to an existing release, upstream's upgrade guard requires
  seeding `containerssh-kata-deploy-state` with `multiInstallSuffix: ""` and
  `deploymentMode: daemonset`, plus Helm ownership metadata, after confirming there is no prior
  Kata installation; see the chart README.
- An end-to-end Kata creation/reconnect check passed on 2026-09-30 using SSH username `kata-smoke`
  (resolves to the default template). Both connections ran under guest kernel `6.18.35` and reused
  Running pod `box-3950ba8a52` with the same UID and `runtimeClassName: kata-qemu-runtime-rs`.
  Only this newly created test box was explicitly deleted afterwards, with a UID precondition.
  The original non-Kata `ubuntu` box (`box-8bbfc143fa`) was explicitly deleted at the owner's
  request on 2026-09-30, with a UID precondition. The session namespace was empty afterwards.
  The owner's next `ssh ubuntu@…` recreated `box-8bbfc143fa` on 2026-09-30 using
  `kata-qemu-runtime-rs` (UID `b2abba32-495f-4212-a16c-b2490144789c`). The new box is Running/Ready
  on worker `container-ssh-worker-k4v2m-mfq8d`; exec verified Kata guest kernel `6.18.35`.
- The config server is pinned to immutable tag `sha-270e2fd` (issue #6), image digest
  `sha256:04597f9f6a11c52662ef874d69eb990435ac790de5952e39be005bef28929b19`;
  auth-server (revision 40, user-facing denial messages) is pinned to
  `sha-245b9e2@sha256:36e68dc2e1686378121a37aebff86ff4c28f42ffe127f9903bae2fe30c0e5f73`
  (previously `sha-72cadaa@sha256:6f1a71ff5c46cc3d88ae967baf9641b057127bddb664118da0fb1644f4fc6baa`).
  The authentik read token and stable host key are mounted
  from existing Secrets (`containerssh-authentik-token`, `containerssh-host-key`).
- Issue #6 was verified live on 2026-09-30: the config webhook returned HTTP 200 for `ubuntu`
  and HTTP 500 for an unmatched username, absent `default` template, and empty username. An
  enrolled-key SSH login as `issue6-no-template` was rejected (exit 255) after config retries,
  with `CONFIG_SERVER_NO_TEMPLATES` logged. Two `ubuntu` SSH reconnects succeeded under Kata
  kernel `6.18.35` and retained pod `box-8bbfc143fa` UID
  `b2abba32-495f-4212-a16c-b2490144789c`. No user pods were created or deleted.
- The revision 18 acceptance check missed prompt rejection: config HTTP 500 errors were retried
  until timeout and SSH only reported connection closure. Revision 19 fixes this through an early
  authz denial. Live checks returned HTTP 200 + `success: false` for unknown, absent `default`, and
  empty template names. The SSH regression (`tests/ssh-template-rejection.py`, with a working
  `ubuntu` positive control) rejected `unknown`, `issue6-fast-test`, and `default` in 0.21–0.23 s
  with `Permission denied`; the unknown test never reached the config webhook. The same existing
  Kata pod UID was preserved, and no user pods were created/deleted. Use `BatchMode=yes` and
  `IdentitiesOnly=yes` for a noninteractive client error; plain SSH may offer password prompts after
  a rejected key even though the server never accepts password authentication.
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

Historical revision-19 follow-up list (not the issue #11 acceptance plan):
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
  2. ✅ Both bundled images are pinned to immutable tags: config-server `sha-270e2fd`,
     auth-server `sha-72cadaa`.
  3. Run the remaining cap-boundary, negative, policy, and fail-closed stages in
     `tests/cluster-deployment-spec.md`.
  4. Optional, later: `ingress.enabled=true` + the one-time Traefik TCP entrypoint/port setup
     (see values.yaml `ingress`, NOTES.txt).

Fresh auth/config smoke-install shape (not a live upgrade; provide published immutable
config-server and settings image values via a reviewed file, plus an ordinary `ubuntu` template):
  ```bash
  helm install containerssh charts/containerssh \
    --kube-context container-ssh --namespace containerssh --create-namespace \
    --set authServer.enabled=true \
    --set authServer.authentik.url=https://authentik.example.com \
    --set authServer.authentik.tokenSecret=containerssh-authentik-token \
    --set configServer.enabled=true \
    -f /secure/path/reviewed-image-and-template-values.yaml
  ```
  then `kubectl --context container-ssh port-forward -n containerssh svc/containerssh 2222:2222`
  and `ssh -p 2222 ubuntu@localhost`.
