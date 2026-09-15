# auth-server — authentik-backed SSH key authentication for ContainerSSH

A Go server implementing the ContainerSSH **authentication webhook** protocol, backed by
authentik for SSH **public-key** lookups. It is built on the official module
(`go.containerssh.io/containerssh`, `auth/webhook` + `config/webhook` packages) and mirrors the
reference server `cmd/containerssh-testauthconfigserver/main.go` — see
[auth-server/spec.md](spec.md) for the design rationale and the authentik API research behind it.

It answers "given an SSH public key, which authentik user owns it?" — something authentik does
**not** do natively (there is no SSH key field on users; see spec §1). The server canonicalizes the
key, fingerprints it, and looks the fingerprint up in the user's `attributes` (spec §4):

```
ssh alice@host ──present key──▶ ContainerSSH ──POST /pubkey──▶ auth-server
                                                                 │ canonicalize (ssh.ParseAuthorizedKey)
                                                                 │ fingerprint (ssh.FingerprintSHA256)
                                                                 ▼
                                     GET /api/v3/core/users/?attributes={"ssh_key_fingerprint":"SHA256:..."}
                                                                 │ Bearer <service token>
                                                                 ▼
                                    authentik  → 0 = deny / 1 = authenticate as that user / >1 = integrity error
```

## Endpoints (one listener, spec §2/§6)

| Path | Meaning |
| --- | --- |
| `POST /password` | username/password auth — **disabled by default**; opt-in test allowlist only |
| `POST /pubkey` | SSH public key auth — the authentik fingerprint lookup |
| `POST /authz` | post-auth authorization — optional group gate (`AUTH_SERVER_REQUIRE_GROUP`) |
| `POST /config` | returns an empty config → ContainerSSH runs its base config unchanged |
| (any other) | `404` |

## How the key lookup works

1. Parse the presented key: `ssh.ParseAuthorizedKey([]byte(publicKey.PublicKey))`.
2. Fingerprint it canonically: `ssh.FingerprintSHA256(key)` → `"SHA256:..."`.
   (ContainerSSH hands us the key already in `ssh.MarshalAuthorizedKey` form — comment-free.)
3. `GET /api/v3/core/users/?attributes={"ssh_key_fingerprint":"SHA256:..."}` (URL-encoded JSON,
   `Authorization: Bearer <token>`) — an **exact scalar match** on the stored attribute.
4. 0 users → deny; exactly 1 → verify username binding then authenticate as the **owner**;
   >1 → integrity error (fingerprints must be unique) → HTTP 500 so ContainerSSH retries.
5. Infrastructure errors (5xx/network) → error → HTTP 500; ContainerSSH keeps retrying until
   its per-method `authTimeout`.

The default attribute is `ssh_key_fingerprint`; the raw key lives in `ssh_public_key` (set by the user via
the authentik settings-flow prompt, or by a provisioner — see spec §5). Both are **derived caches**:
users keep entering keys, and the background sync (below) keeps the fingerprint in sync.

### Alternative lookup attribute (`AUTH_SERVER_KEY_ATTRIBUTE`)

The lookup attribute is configurable, which helps when your authentik users keep their keys in a
different attribute — e.g. the raw `sshPublicKey`:

- unset / `ssh_key_fingerprint` (default): exact-match filter on the derived fingerprint cache —
  one cheap API call. The production path.
- any other value (e.g. `sshPublicKey`): the attribute holds the armored key material. The server
  first probes the exact-match `attributes` filter with the canonical key — scalar, then
  list-wrapped (authentik matches the attribute value as JSON with exact equality; a list-stored
  attribute only matches an exactly-equal list query) — and falls back to scanning every user and
  fingerprint-comparing each stored key line (handles comments, line breaks, and multi-key list
  values). Integrity rules are identical: a key bound to more than one user is a 500, not an
  accidental allow. Note the fallback walks every user page — for large directories store the
  **canonical key without comment** (scalar, or a single-element list) so the exact probe hits, or
  use the fingerprint index.
- The authentik users API has no per-attribute query parameter (e.g. `?sshPublicKey=…`): unknown
  query params are silently ignored and return the full directory. The documented filter is the
  JSON `attributes` parameter (verified against authentik 2026.5.2).

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `CONTAINERSSH_LISTEN` | `0.0.0.0:8080` | Listen address |
| `CONTAINERSSH_LOG_LEVEL` | `6` | Syslog-style: `7` debug, `6` info, `5` notice, `4` warning, `3` error, `2` crit |
| `CONTAINERSSH_TLS_CERT` | — | Server certificate (file path or PEM) → enables HTTPS |
| `CONTAINERSSH_TLS_KEY` | — | Server private key (file path or PEM) |
| `CONTAINERSSH_TLS_CLIENTCA` | — | CA to verify clients (enables mTLS) |
| `AUTHENTIK_URL` | **required** | authentik base URL, e.g. `https://authentik.example.com` |
| `AUTHENTIK_TOKEN` / `AUTHENTIK_TOKEN_FILE` | **required** | service-account token with **view users** (each other; `_FILE` wins, trims trailing newline) |
| `AUTHENTIK_WRITE_TOKEN` / `AUTHENTIK_WRITE_TOKEN_FILE` | falls back to read token | optional separate token with **edit users** for the sync; keep it separate for least privilege |
| `AUTHENTIK_CA_FILE` | — | custom CA PEM (file path or literal) for a self-signed authentik |
| `AUTHENTIK_INSECURE_SKIP_VERIFY` | `false` | `1/true` → skip TLS verification (dev only) |
| `AUTH_SERVER_ENFORCE_USERNAME` | `true` | require `ssh <user>@host` to equal the authentik **owner** of the key (prevents impersonation with a stolen key + known username) |
| `AUTH_SERVER_PASSWORD_USERS` | — | comma-separated usernames allowed to use password auth (any password — **test/break-glass only, not verified**) |
| `AUTH_SERVER_REQUIRE_GROUP` | — | group name; when set, `OnAuthorization` demands membership before session starts |
| `AUTH_SERVER_KEY_ATTRIBUTE` | `ssh_key_fingerprint` | authentik attribute the presented key is looked up by; set e.g. `sshPublicKey` to match raw key material (see above) |
| `AUTH_SERVER_SYNC_INTERVAL` | — (off) | normalizing sync interval, e.g. `30s`, `5m` (spec §5.3 option (a)) |
| `AUTH_SERVER_SYNC_WRITE` | `true` | `false` runs the sync in dry-run (report only, no PATCHes) |

> Note: wildcard/`_FILE` tokens are read with no trailing newline via `strings.TrimSpace`.

## The normalizing + fingerprint sync (spec §5.3)

Users paste keys in *My settings* — freeform, with comments, maybe line-wrapped. That is exactly the
fragile case the API's **exact** `attributes` filter can't match reliably. So, on
`AUTH_SERVER_SYNC_INTERVAL`, the server walks all users and for anyone with
`attributes.ssh_public_key` set:

- parses the armored key, drops comments/whitespace, re-marshals a canonical line;
- computes `ssh.FingerprintSHA256`;
- `PATCH /api/v3/core/users/{id}/` (id = integer `pk`) to store both `attributes.ssh_public_key`
  (canonical) and `attributes.ssh_key_fingerprint` — only when they changed;
- flags (does not write) a fingerprint shared by **two different users** — key uniqueness violation.

`OnPubKey` stays a fast exact-match query against the derived fingerprint attribute. The sync is
*optional*: with the read token able to edit users it persists; with a read-only token
(or `AUTH_SERVER_SYNC_WRITE=0`) it just reports what it would change. Never deletes attributes.

## Build & run locally

```bash
go build -o auth-server .          # or: go run .
go test ./...                      # unit tests against an in-memory authentik mock

# point it at your authentik with a service token:
AUTHENTIK_URL=https://authentik.example.com \
AUTHENTIK_TOKEN_FILE=$(pwd)/token \
CONTAINERSSH_LISTEN=127.0.0.1:8080 \
CONTAINERSSH_LOG_LEVEL=7 \
AUTH_SERVER_SYNC_INTERVAL=30s ./auth-server

# simulate SSH key auth:
curl -s -X POST http://127.0.0.1:8080/pubkey -H 'Content-Type: application/json' \
  -d '{"username":"alice","connectionId":"c1","remoteAddress":"10.0.0.1:43210",
       "clientVersion":"SSH-2.0-OpenSSH_9.6",
       "publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... "}'
```

## Authentik prerequisites

- A **service account token** with `authentik_api_view`/read on users (plus edit if the sync writes).
- Users have `attributes.ssh_public_key` (+ derived `ssh_key_fingerprint`). Two ways:
  1. **Self-service:** the `default-user-settings-flow` Prompt writing to
     `attributes.ssh_public_key` (spec §5.2) — users manage their own key.
  2. **Provisioner:** any script `PATCH`es the attributes directly.
- Optional group for the authz gate: create the group and add members.

## Deploy in Kubernetes

Enable it from the ContainerSSH chart (`authServer.enabled=true`) — it deploys this server as a
sibling Deployment + Service, creates a Secret for the authentik token, and auto-wires
`auth.password.webhook.url`, `auth.publicKey.webhook.url` and `auth.authz.webhook.url` at
`http://<release>-auth-server.<ns>.svc.cluster.local:8080` (spec §3). See `charts/containerssh/`.

The repo CI (`.github/workflows/auth-server-image.yml`) builds this directory and publishes it to
`ghcr.io/tu-wien-datalab/dev.box/auth-server` on push to `main`; the chart's `authServer.image`
defaults to that image. Pull it into the cluster like the config-server image (public package or an
`imagePullSecret`).

For a standalone deployment: build & push the image, then run it with the env vars above and point
ContainerSSH's auth sections at it.

## Security notes

- Read-only against authentik for auth (only the optional sync writes) → the API token is exposed
  only to this pod.
- Fail-closed: if authentik is unreachable, `OnPubKey` returns 500 and ContainerSSH denies the
  connection rather than allowing it.
- Firewall the server: ContainerSSH → HTTP(S) (add mTLS via the `CONTAINERSSH_TLS_*` variables +
  chart `authServer.cacert`/`cert`/`key`), no inbound from the internet.
- Password auth is off by default and cannot verify passwords against authentik — it exists solely
  as an explicitly-named testing escape hatch. Keep it off in production.
