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

The value must be:

- a JSON list;
- exactly one item;
- a valid canonical SSH public key in `type + base64` form;
- free of comments and surrounding whitespace.

Authentik's users API supports exact JSON attribute equality:

```http
GET /api/v3/core/users/?attributes={"sshPublicKey":["ssh-ed25519 AAAA..."]}
```

The `attributes` value is URL-encoded on the wire. List membership, substring matching, and
case-insensitive matching are not supported. Consequently, scalar values, commented keys, and
multi-key lists do not match.

### Upload requirement

Users should provide their ordinary `.pub` key. The authentik settings flow or provisioner must
validate/canonicalize it before storing it by retaining only the key type and base64 payload. The
auth server cannot normalize stored values because its token is read-only and the users API only
supports exact equality.

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

1. Parse the presented key with `ssh.ParseAuthorizedKey`.
2. Re-marshal it with `ssh.MarshalAuthorizedKey` and trim whitespace, producing `type + base64`.
3. Make one authenticated request to authentik:

   ```http
   GET /api/v3/core/users/?attributes={"sshPublicKey":["<canonical-key>"]}
   Authorization: Bearer <read-token>
   ```

4. Require both the reported result count and decoded result length to equal one.
5. Require the owner account to be active.
6. If username enforcement is enabled, require the requested SSH username to equal the authentik
   username, case-insensitively.
7. Authenticate as the authentik owner, not merely as the requested SSH username.

Decision table:

| Condition | Result |
| --- | --- |
| malformed key | deny |
| zero users | deny |
| multiple users | deny |
| one inactive user | deny |
| username mismatch while enforcement is enabled | deny |
| one active owner satisfying username policy | authenticate as owner |
| timeout, transport error, non-2xx, or invalid response | HTTP 500 / fail closed |

## HTTP and security requirements

- Authentik requests have a bounded timeout.
- The bearer token requires read access to users only.
- The complete request URL is never logged because it contains the public key query.
- Decision logs may contain requested username, owner username, and duration, but not key material.
- TLS verification is enabled by default; a custom CA is supported.
- `AUTHENTIK_INSECURE_SKIP_VERIFY` is development-only.

## Other endpoints

The same listener exposes the protocol-complete endpoints:

| Endpoint | Behavior |
| --- | --- |
| `POST /password` | deny unless explicitly enabled by the test-only allowlist |
| `POST /pubkey` | public-key flow above |
| `POST /authz` | allow, or apply the configured authentik group gate |
| `POST /config` | return an empty override so ContainerSSH keeps its base config |
| other path or method | `404` or `405` |

## Configuration

Required:

- `AUTHENTIK_URL`
- `AUTHENTIK_TOKEN` or `AUTHENTIK_TOKEN_FILE`

Public-key policy:

- `AUTH_SERVER_ENFORCE_USERNAME` defaults to `true`.

There is intentionally no configurable key attribute, write token, synchronization interval, or
fingerprint mode. The single `attributes.sshPublicKey` contract keeps the login path deterministic.
