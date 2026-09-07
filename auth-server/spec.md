# Auth Server — ContainerSSH + authentik SSH key lookup

This spec covers two things:

1. **What the authentik API can and cannot do** for the "given an SSH public key, find the user" lookup.
2. **How to build/configure a ContainerSSH auth server** that performs that lookup, using the official
   `auth/webhook` (and `config/webhook`) libraries shipped with ContainerSSH, with
   `cmd/containerssh-testauthconfigserver/main.go` as the working reference.

---

## 1. authentik API capability (verified)

All facts below were verified against:

- `https://api.goauthentik.io/schema.yml` (OpenAPI v3, version **2026.11.0-rc1**)
- `authentik/core/models.py`, `authentik/core/api/users.py`, `authentik/core/api/groups.py` (main branch)
- `web/src/*` frontend source and the release notes

### 1.1 authentik does NOT store SSH public keys on users

- The `User` model/serializer has **no** `public_key` field. Fields are: `username`, `name`, `email`,
  `attributes`, `groups`, `roles`, `path`, `type`, `is_active`, timestamps, etc.
- The only `public_key` fields in the entire API belong to the **CaptchaStage** (captcha provider keys) —
  unrelated to SSH.
- The only `"ssh"` string in the schema is a value of `ProtocolEnum` (`rdp`, `vnc`, `ssh`) used by the
  **RAC provider** (remote-access proxying), not user key storage.
- The web UI contains no "SSH" user-settings field, and the release notes mention SSH only in the RAC
  context. There is **no first-class "SSH Public Key" setting in authentik** — if one appears in an
  admin UI it is a custom user attribute, not a built-in.

**Conclusion:** the reverse lookup *"given a public key, find the user"* is **not** a built-in authentik
capability. It must be implemented by us (Section 2/3).

### 1.2 Extra user details: YES — the `attributes` JSON field

Every authentik user carries an arbitrary JSON object:

- Read: `GET /api/v3/core/users/{id}/` → `attributes: { ... }` (free-form object)
- Write: `POST/PATCH /api/v3/core/users/` (`UserRequest`) → set `attributes` on create/update.
  ```json
  { "username": "alice", "name": "Alice", "attributes": { "ssh_key_fingerprint": "SHA256:..." } }
  ```
- Group attributes merge via `User.group_attributes()` and flow through property mappings/flows.

This is our storage mechanism for SSH key material.

### 1.3 Searching by an attribute value: YES, exact only

`GET /api/v3/core/users/?attributes=<JSON>` — verified implementation in `UsersFilter`:

```python
# parses value as JSON (must be a dict), then:
queryset.filter(attributes__<key> = <value>)   # all pairs AND-ed
```

Semantics:

| Want to… | Supported |
|---|---|
| Exact match on one attribute key | ✅ `?attributes={"ssh_key_fingerprint":"SHA256:AAa..."}` |
| Several attribute pairs (AND) | ✅ `?attributes={"a":"1","b":"2"}` |
| Substring / `icontains` | ❌ equality only, case-sensitive |
| "value is inside a stored list" | ❌ a list matches only the **whole array** exactly |
| Match by value without knowing the key | ❌ the exact attribute key is required |

The identical filter exists on `GET /api/v3/core/groups/?attributes=...`.

**Practical consequence:**
- A **single SSH key stored as a scalar string** is lookup-able today via exact match.
- Do **not** match the raw armored SSH key string (`ssh-ed25519 AAAA... [comment]`) — comments and
  whitespace make exact matches brittle. Match on a canonical **fingerprint** instead.
- When users get a second key, the scalar approach breaks (list membership isn't supported); plan for a
  reverse index (fingerprint → user) at that point.

### 1.4 Recommended authentik data model

Store one fingerprint per user, computed with the same canonical format ContainerSSH produces:

| authentik user attribute | value |
|---|---|
| `ssh_public_key` | armored key as entered (set by the user via the settings flow, §5) |
| `ssh_key_fingerprint` | `SHA256:...` (derived; kept in sync by the normalizing job, §5.3) |

Write path (§5):
- **User self-service (preferred for this setup):** a Prompt added to the default user-settings flow
  lets each user set their key in *My settings*; it lands in `attributes.ssh_public_key`.
- **Admin/provisioner:** a script/operator/job sets `attributes` via `PATCH /api/v3/core/users/{id}/`
  when keys change.

The auth server only ever **reads** them.

Read path (at connection time):

```http
GET /api/v3/core/users/?attributes={"ssh_key_fingerprint":"SHA256:lC0iSbg0..."}
Authorization: Bearer <service-token>
```

Exact match → the returned user owns that key. (The JSON value must be URL-encoded.)

---

## 2. Building the ContainerSSH auth/config server

### 2.1 What ContainerSSH ships

- **`go.containerssh.io/containerssh/auth/webhook`** — a ready-made HTTP server implementing the
  ContainerSSH auth protocol. It exposes three endpoints:
  - `POST /password` — username/password auth
  - `POST /pubkey` — SSH public key auth
  - `POST /authz` — post-auth authorization
- **`go.containerssh.io/containerssh/config/webhook`** — the matching config server, exposing
  `POST /config` (per-user/connection app config).
- **Working reference:** `cmd/containerssh-testauthconfigserver/main.go` in the ContainerSSH repo
  (ships an `authconfig` server with all four endpoints on `0.0.0.0:8080`).

### 2.2 The interface you implement

```go
import (
    "go.containerssh.io/containerssh/auth"
    "go.containerssh.io/containerssh/metadata"
)

type AuthRequestHandler interface {
    OnPassword(meta metadata.ConnectionAuthPendingMetadata, password []byte) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
    OnPubKey(meta metadata.ConnectionAuthPendingMetadata, publicKey auth.PublicKey) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
    OnAuthorization(meta metadata.ConnectionAuthenticatedMetadata) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
}
```

Contract:
- Return `true` + authenticated metadata on success; `false` (+ `meta.AuthFailed()`) on failure.
- Return an `error` only for infrastructure failures (e.g. authentik unreachable) → server replies HTTP 500.
- `meta.Authenticated(username)` sets the verified `authenticatedUsername` (may differ from the
  user-supplied `meta.Username`).

### 2.3 The wire protocol (JSON)

`POST /pubkey` request body = connection metadata inline + the key:

```json
{
  "remoteAddress": { "IP": "192.168.1.10", "Port": 51734 },
  "connectionId": ".....",
  "clientVersion": "SSH-2.0-OpenSSH_9.6",
  "username": "alice",
  "publicKey": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."
}
```

`POST /password` request body has the same metadata plus the Base64-encoded password:

```json
{ "remoteAddress": { ... }, "connectionId": "...", "clientVersion": "...", "username": "alice",
  "passwordBase64": "aHVudGVyMmh1bnRlcjI=" }
```

Response body (both, plus `/authz`):

```json
{
  "success": true,
  "remoteAddress": { "IP": "192.168.1.10", "Port": 51734 },
  "connectionId": ".....",
  "clientVersion": "SSH-2.0-OpenSSH_9.6",
  "username": "alice",
  "authenticatedUsername": "alice",
  "metadata": {}, "environment": {}, "files": {}
}
```

`/config` request = authenticated metadata (same shape minus `success`); response = `{ <metadata>,
"config": <AppConfig> }` where `AppConfig` carries `docker`/`kubernetes`/`ssh` execution settings
(see the reference `OnConfig`, which sets a busybox image / shell command).

### 2.4 Wiring it up (mirrors `cmd/containerssh-testauthconfigserver/main.go`)

```go
// 1. handlers implement the interfaces above
authHandler := authWebhook.NewHandler(&authHandler{}, logger)        // http.Handler for /password, /pubkey, /authz
configHandler, _ := configWebhook.NewHandler(&configHandler{}, logger) // http.Handler for /config

// 2. route the four paths on one mux (reference's `handler.ServeHTTP`)
//    /password, /pubkey → authHandler ; /config → configHandler ; else 404

// 3. run behind the library HTTP server + service lifecycle
srv, _ := http.NewServer("authconfig",
    config.HTTPServerConfiguration{ Listen: "0.0.0.0:8080" },
    mux, logger, func(string) {})
lifecycle := service.NewLifecycle(srv)
lifecycle.Run()
```

Alternatively `authWebhook.NewServer(cfg, handler, logger)` returns a complete server directly
(minus the `/config` endpoint, which stays on `configWebhook`).

### 2.5 Deployment notes

- Run it next to (or on the same host as) ContainerSSH; ContainerSSH calls it over HTTP(S).
- Pair with mTLS via `cacert`/`cert`/`key` if not on localhost (ContainerSSH client config below).
- Log at debug with LJSON (as the reference) for troubleshooting auth decisions.

---

## 3. ContainerSSH-side configuration (how ContainerSSH calls this server)

In the ContainerSSH config, point the webhook clients at the server; every `url` uses the HTTP client
settings (`timeout` default 2s, plus optional TLS/mTLS). Example YAML:

```yaml
auth:
  password:
    webhook:
      url: http://127.0.0.1:8080
      timeout: 2s      # per-HTTP-call timeout
      authTimeout: 60s # total auth attempt budget (ContainerSSH retries on non-200)
  pubkey:
    webhook:
      url: http://127.0.0.1:8080
      timeout: 2s
      authTimeout: 60s
  authz:
    webhook:
      url: http://127.0.0.1:8080
      timeout: 2s
      authTimeout: 60s

configserver:
  webhook:
    url: http://127.0.0.1:8080
      timeout: 2s

# optional (shared HTTPClientConfiguration on each webhook block) mutual TLS
# when the server is remote:
#   cacert: /path/to/ca.pem
#   cert: /path/to/client.pem
#   key: /path/to/client-key.pem
#   tlsVersion: "1.3"
```

---

## 4. Putting it together: authentik-backed `OnPubKey`

Suggested implementation inside the auth handler:

1. Parse the presented key: `ssh.ParsePublicKey([]byte(publicKey.PublicKey))`.
2. Fingerprint it canonically: `ssh.FingerprintSHA256(key)` → `"SHA256:..."` (golang.org/x/crypto/ssh;
   the same format ContainerSSH uses for host keys).
3. Query authentik:
   ```http
   GET /api/v3/core/users/?attributes={"ssh_key_fingerprint":"SHA256:..."}
   Authorization: Bearer <service-token>
   ```
   (URL-encode the `attributes` JSON; exact match on a scalar attribute — see §1.3.)
4. Look at the returned page:
   - 0 users → `return false, meta.AuthFailed(), nil`
   - 1 user → verify/enforce username binding, then `return true, meta.Authenticated(username), nil`
   - >1 → treat as integrity error (fingerprints should be unique).
5. On API failure (non-2xx / network) → `return false, meta.AuthFailed(), err` (→ 500, ContainerSSH
   retries until its timeout as configured).
6. Authorization hardening: also implement `OnAuthorization` to gate on group membership / source of the
   discovered user.

### Multi-key future-proofing
When a user can hold several keys, scalar `ssh_key_fingerprint` no longer suffices (list-membership is
not queryable). Migrate to a caller-side reverse index (fingerprint → username) updated from authentik
on change, and make `OnPubKey` read that. Design the current single-key path so it can swap to this
behind the same `OnPubKey` signature.

---

## 5. authentik configuration — self-service SSH key attribute

So users manage their own key instead of an admin/provisioner writing it: add a **Prompt** to the
default **user settings** flow. The value lands in `user.attributes` and is then queryable via the
ordinary API (see §1.3).

> ⚠️ `default-user-settings-field-ssh-pub-key` is **not** part of upstream authentik (not in `main` or
> `version/2026.8.0` blueprints). It exists in **our** instance already, so copy it there as-is — it
> already has the right type/label and we only adjust the bits below.

### 5.1 How Prompts write data (so you configure it correctly)

- A **Prompt** (_Flows & Stages → Prompts_) is a single named form field; model
  `authentik_stages_prompt.prompt`. Relevant fields:
  - `name` — unique identifier (e.g. `default-user-settings-field-ssh-pub-key`).
  - `field_key` — **where the value is stored.** Plain keys write to user fields
    (`username`, `name`, `email` — see the defaults); keys prefixed `attributes.` write into the
    user's `attributes` JSON (the default locale prompt uses
    `field_key: attributes.settings.locale`). For us: `field_key: attributes.ssh_public_key`.
  - `type` — from `PromptTypeEnum` (`text`, `text_area`, `text_read_only`, `text_area_read_only`,
    `email`, `ak-locale`, …).
  - `label`, `placeholder`, `sub_text`, `required`, `order`, `initial_value` (+ `*_expression` flags).
- A **PromptStage** (`authentik_stages_prompt.promptstage`) groups Prompts into one rendered form; the
  **UserWriteStage** (`authentik_stages_user_write.userwritestage`, `user_creation_mode: never_create`)
  writes the submitted `prompt_data` back to the user. In the default flow these are
  `default-user-settings` (prompt stage) followed by `default-user-settings-write`.

### 5.2 Manual steps (admin UI)

1. **Copy the existing prompt** — _Flows & Stages → Prompts_ → find
   `default-user-settings-field-ssh-pub-key` → **Duplicate**/copy.
2. **Point the copy at the new attribute:**

   | field | value |
   |---|---|
   | `name` | new unique identifier (e.g. `user-settings-field-ssh-public-key`) |
   | `field_key` | `attributes.ssh_public_key` |
   | `type` | `text_area` |
   | `label` | `SSH public key` |
   | `placeholder` | `ssh-ed25519 AAAA... (get it with: ssh-keygen -t ed25519)` |
   | `required` | `false` (don't force users to have a key) |
   | `order` | `204` (after the default fields 200–203) |

   The copied prompt may already carry the right `type`/`label`; adjust `name`, `field_key`, and
   `order` at minimum.
3. **Add the prompt to the flow** — _Flows & Stages → Flows → `default-user-settings-flow` → Edit._
   Add the new prompt to the `default-user-settings` prompt stage (its `fields` list). If you instead
   wire it as a new stage, you must insert a FlowStageBinding with an `order` **before**
   `default-user-settings-write` (order `100`) — the write stage always runs after the prompt stage,
   otherwise nothing is persisted.
4. **Done.** Users now see the field under *My settings → Update your info*; the value is stored in
   `user.attributes.ssh_public_key`, visible and queryable via the API.

### 5.3 Consequence for the lookup design

Self-service stores the **raw armored key** (`ssh-ed25519 AAAA... [comment]`) — exactly the
fragile-exact-match case from §1.3 (comments/line-wrapping mean the API `attributes` filter won't
reliably match a key as presented by the SSH client, which has no comment). Keep the fast
fingerprint lookup from §4 working with one of:

- **(a) Normalize + fingerprint in a background sync (recommended).** A job (part of the auth server,
  or cron/script) periodically: for each user, read `attributes.ssh_public_key` → canonicalize
  (`ssh.ParseAuthorizedKey` + `ssh.MarshalAuthorizedKey`, dropping any comment) → compute
  `ssh.FingerprintSHA256` → `PATCH /api/v3/core/users/{id}/` setting both `attributes.ssh_public_key`
  (canonical) and `attributes.ssh_key_fingerprint`. `OnPubKey` stays a fingerprint exact-match query;
  the fingerprint attribute is a derived cache.
- **(b) On-demand fallback.** `OnPubKey` queries `?attributes={"ssh_key_fingerprint":"..."}` first;
  on no hit, pull users and compare against `attributes.ssh_public_key` in code (works, but scales
  worse and needs careful normalization).
- **(c) Sanitize at the UI** is unreliable (users paste freeform); let the sync in (a) keep data tidy.

Enforce **uniqueness** in whatever writes fingerprints: the same key/fingerprint must never be bound to
more than one user (the sync should flag/act on duplicates).

### 5.4 Updated write path vs. §1.4

§1.4 earlier assumed a provisioner writes the attributes. With the settings-flow Prompt, the write path
is **user self-service** (key set in *My settings*), and the only automated writer is the optional
normalizing sync above. The auth server remains read-only against the API either way.

## 6. Cheat sheet

| Question | Answer |
|---|---|
| Store extra user data in authentik? | Yes — `attributes` JSON field (read+write via API) |
| Search users by attribute value? | Yes, **exact** match per key: `?attributes={"key":"value"}` |
| Substring / list-membership search? | No |
| Search an SSH public key "by value"? | Not natively; store a canonical **fingerprint** and match that |
| Who computes the lookup? | The ContainerSSH auth server (this project) |
| User self-service key setting | Prompt in `default-user-settings-flow` (§5) |
| ContainerSSH auth-server library | `go.containerssh.io/containerssh/auth/webhook` |
| Config-server library | `go.containerssh.io/containerssh/config/webhook` |
| Working reference | `cmd/containerssh-testauthconfigserver/main.go` |
| Endpoints to expose | `/password`, `/pubkey`, `/authz`, `/config` |
