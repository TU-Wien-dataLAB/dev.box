# Auth server specification

## Purpose

The auth server implements ContainerSSH's authentication webhook protocol and resolves an SSH public
key to its owning authentik user.

The public-key login path is deliberately read-only and constant-cost: one exact authentik query per
attempt. It does not maintain a fingerprint index, scan users, or retry with alternate key shapes.

## Authentik data contract

Authentik has no built-in SSH-key field, but every user has an arbitrary `attributes` JSON object.
The user's SSH key is stored under `sshPublicKey`:

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

The implementation uses `go.containerssh.io/containerssh/auth/webhook` and implements:

```go
type AuthRequestHandler interface {
    OnPassword(meta metadata.ConnectionAuthPendingMetadata, password []byte) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
    OnPubKey(meta metadata.ConnectionAuthPendingMetadata, publicKey auth.PublicKey) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
    OnAuthorization(meta metadata.ConnectionAuthenticatedMetadata) (
        bool, metadata.ConnectionAuthenticatedMetadata, error)
}
```

For public-key authentication:

- success returns `true` and `meta.Authenticated(owner.Username)`;
- an authentication miss returns `false`, `meta.AuthFailed()`, and no error;
- an authentik/network failure returns an error so the webhook responds with HTTP 500 and
  ContainerSSH fails closed.

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
| `POST /password` | always deny; present only to satisfy the handler interface |
| `POST /pubkey` | public-key flow above |
| `POST /authz` | always allow; present only to satisfy the handler interface |
| other path | `404` |

## Configuration

Required:

- `AUTHENTIK_URL`
- `AUTHENTIK_TOKEN` or `AUTHENTIK_TOKEN_FILE`

There is intentionally no configurable key attribute, username-binding mode, password allowlist,
group gate, write token, synchronization interval, fingerprint mode, custom-CA setting, or
server-side TLS/mTLS configuration. The single
`attributes.sshPublicKey` contract keeps the login path deterministic.
