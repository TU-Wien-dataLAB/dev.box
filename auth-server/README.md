# auth-server — authentik-backed SSH key authentication

A small ContainerSSH authentication webhook that answers one question:

> Which authentik user owns this SSH public key?

For `POST /pubkey`, it validates and canonicalizes the presented key, then performs one exact
query against authentik:

```http
GET /api/v3/core/users/?attributes={"sshPublicKey":["ssh-ed25519 AAAA..."]}
Authorization: Bearer <service-token>
```

The JSON query is URL-encoded on the wire. There is no fingerprint cache, background sync, fallback
query, or directory scan.

## Public-key contract

Each authentik user may have one key in `attributes.sshPublicKey`. The value must be a
single-element JSON list containing the canonical public key:

```json
{
  "sshPublicKey": ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."]
}
```

Canonical means `type + base64` only. Optional comments such as `alice@laptop` are not stored because
SSH clients do not transmit comments during authentication. If the authentik settings field accepts
a pasted `.pub` line, its write path must strip the optional comment before saving.

Authentication behavior:

- exactly one matching active user: authenticate as that authentik user;
- zero or multiple users: deny cleanly;
- malformed key: deny cleanly;
- authentik/network failure: return HTTP 500 so ContainerSSH fails closed;
- with username enforcement enabled, the requested SSH username must match the owner.

## Endpoints

| Path | Meaning |
| --- | --- |
| `POST /pubkey` | authentik-backed SSH public-key authentication |
| `POST /password` | disabled by default; optional unverified test allowlist |
| `POST /authz` | optional authentik group gate |
| `POST /config` | empty config; ContainerSSH retains its base configuration |

The server uses ContainerSSH's official `auth/webhook` and `config/webhook` packages.

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `CONTAINERSSH_LISTEN` | `0.0.0.0:8080` | Listen address |
| `CONTAINERSSH_LOG_LEVEL` | `6` | Syslog style: `7` debug through `2` critical |
| `CONTAINERSSH_TLS_CERT` | — | Server certificate path or PEM; enables HTTPS |
| `CONTAINERSSH_TLS_KEY` | — | Server private-key path or PEM |
| `CONTAINERSSH_TLS_CLIENTCA` | — | CA used to verify clients; enables mTLS |
| `AUTHENTIK_URL` | **required** | authentik base URL |
| `AUTHENTIK_TOKEN` / `AUTHENTIK_TOKEN_FILE` | **required** | token with read access to users; `_FILE` wins |
| `AUTHENTIK_CA_FILE` | — | custom CA path or PEM |
| `AUTHENTIK_INSECURE_SKIP_VERIFY` | `false` | skip TLS verification; development only |
| `AUTH_SERVER_ENFORCE_USERNAME` | `true` | require requested SSH username to equal the key owner |
| `AUTH_SERVER_PASSWORD_USERS` | — | test-only usernames allowed with any password |
| `AUTH_SERVER_REQUIRE_GROUP` | — | optional authentik group required after authentication |

## Build and test

```bash
go build ./...
go vet ./...
go test -race ./...
```

Run locally:

```bash
AUTHENTIK_URL=https://authentik.example.com \
AUTHENTIK_TOKEN_FILE=$(pwd)/token \
CONTAINERSSH_LISTEN=127.0.0.1:8080 \
./auth-server
```

The repository workflow `.github/workflows/auth-server-image.yml` publishes
`ghcr.io/tu-wien-datalab/dev.box/auth-server` on pushes to the default branch.

## Deploy with the Helm chart

```yaml
authServer:
  enabled: true
  authentik:
    url: https://authentik.example.com
    tokenSecret: containerssh-authentik-token
```

The chart deploys the webhook and wires ContainerSSH's password, public-key, and authorization
webhook URLs to it. The authentik token only needs read access to users.

## Security properties

- The server is read-only against authentik.
- Authentication fails closed on API and network failures.
- Malformed, unknown, duplicate, and inactive-user keys are denied.
- Full public keys are not written to decision or error logs.
- The authenticated ContainerSSH identity is the authentik owner, even when requested-username
  enforcement is disabled.
