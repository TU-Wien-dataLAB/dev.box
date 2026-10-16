# Auth server specification

## Purpose

The auth server implements ContainerSSH's authentication webhook protocol and resolves an SSH public
key to its owning authentik user.

The public-key login path is deliberately read-only and constant-cost: one exact authentik query per
attempt. It does not maintain a fingerprint index, scan users, or retry with alternate key shapes.

## Authentik data contract

Authentik has no built-in SSH-key field, but every user has an arbitrary `attributes` JSON object.
The user's SSH key is stored under `sshPublicKey` (configurable via `AUTH_SERVER_KEY_ATTRIBUTE`):

```json
{
  "sshPublicKey": ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."]
}
```

The value must be a JSON list with exactly one item, and that item must exactly equal the public-key
string supplied by ContainerSSH. During normal SSH authentication ContainerSSH supplies key type and
base64 payload without the optional `.pub` comment.

Authentik's users API supports exact JSON attribute equality:

```http
GET /api/v3/core/users/?attributes={"sshPublicKey":["ssh-ed25519 AAAA..."]}
```

The `attributes` value is URL-encoded on the wire. List membership, substring matching, and
case-insensitive matching are not supported. Consequently, scalar values and multi-key lists do not
match. Comments are not treated specially: if present in the webhook value, they participate in the
exact match.

### Upload requirement

Users provide their ordinary public key through the authentik settings field. The stored string must
match ContainerSSH's webhook value. The auth server is deliberately read-only and does not parse or
rewrite stored keys.

This version supports one key per user. Supporting multiple keys requires a different authentik data
model with an exact-searchable reverse index.

## ContainerSSH webhook contract

The local HTTP adapter in `webhook.go` uses ContainerSSH's request/response types and adds
an optional `message` field to policy denials. The stock v0.6.0 `auth/webhook` handler cannot
serialize that field. Internal callbacks return an `authDecision` (success, authenticated metadata,
and denial message) plus an error, rather than implementing the stock SDK handler interface.

| Decision | HTTP response |
| --- | --- |
| Active, uniquely enrolled owner | 200, `success: true`, authenticated as owner, no `message` |
| Policy denial | 200, `success: false`, user-facing `message` ending in a newline |
| Authentik/network failure | 500, `success: false`, no `message` |
| Malformed request | 500, `success: false`, no `message` |
| Unknown endpoint | 404 |

Policy messages cover unsupported passwords, non-unique/absent key enrollment, inactive accounts,
unknown templates, and missing group membership. They never contain key material, passwords,
tokens, or infrastructure errors.

**Gateway compatibility:** message-bearing denials require the fork's
[PR #10](https://github.com/TU-Wien-dataLAB/ContainerSSH/pull/10), which delivers the message as
an SSH userauth banner before a terminal authentication failure. The chart pins a published
PR #10 image in `values.yaml`; this is not a live rollout. Stock v0.6.0 (including the current
unpatched `deploy` image) strictly rejects unknown JSON fields: it does not ignore `message`,
and will retry instead of promptly denying. Overridden gateway images must include the patch.
Successes and message-less responses retain the stock wire format.

## Public-key decision flow

For each `POST /pubkey` request:

1. Take the public-key string supplied by ContainerSSH unchanged.
2. Make one authenticated request to authentik:

   ```http
   GET /api/v3/core/users/?attributes={"sshPublicKey":["<public-key>"]}
   Authorization: Bearer <read-token>
   ```

3. Require both the reported result count and decoded result length to equal one.
4. Require the owner account to be active.
5. Authenticate as the authentik owner. The requested SSH username is independent and selects the
   dev.box pod template.

Decision table:

| Condition | Result |
| --- | --- |
| zero users | deny |
| multiple users | deny |
| one inactive user | deny |
| one active owner | authenticate as owner |
| timeout, transport error, non-2xx, or invalid response | HTTP 500 / fail closed |

## HTTP and security requirements

- Authentik requests have a bounded timeout.
- The bearer token requires read access to users only.
- The complete request URL is never logged because it contains the public key query.
- Decision logs may contain requested username, owner username, and duration, but not key material.
- TLS verification uses the container's system CA trust store by default.
- `AUTHENTIK_INSECURE_SKIP_VERIFY` is development-only.

## Other endpoints

The same listener exposes the protocol-complete endpoints:

| Endpoint | Behavior |
| --- | --- |
| `POST /password` | always deny; protocol-required endpoint |
| `POST /pubkey` | public-key flow above |
| `POST /authz` | apply the configured template-name allowlist and authentik group gate; allow if both gates pass or are disabled |
| other path | `404` |

## Configuration

Required:

- `AUTHENTIK_URL`
- `AUTHENTIK_TOKEN` or `AUTHENTIK_TOKEN_FILE`

Optional authorization policy: `AUTH_SERVER_ALLOWED_TEMPLATES` is a JSON array of exact SSH
username/template names (unset disables the gate; `[]` denies all). The bundled chart sets it from
`kubernetes.podTemplates` when both servers are enabled. Template denials return HTTP 200 with
`success: false`, not an error that triggers ContainerSSH retries. `AUTH_SERVER_REQUIRE_GROUP`
continues to apply after template selection.

There is intentionally no fingerprint mode, username-binding mode, password allowlist, write token,
synchronization interval, custom-CA setting, or server-side TLS/mTLS configuration. The attribute
name is configurable (`AUTH_SERVER_KEY_ATTRIBUTE`); the lookup itself stays a single exact query.
The default `attributes.sshPublicKey` contract keeps the login path deterministic.
