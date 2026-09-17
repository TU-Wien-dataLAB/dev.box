# auth-server — authentik-backed SSH key authentication

A small ContainerSSH authentication webhook that answers one question:

> Which authentik user owns this SSH public key?

For `POST /pubkey`, it performs one exact authentik query for the public-key string supplied by
ContainerSSH:

```http
GET /api/v3/core/users/?attributes={"sshPublicKey":["ssh-ed25519 AAAA..."]}
Authorization: Bearer <service-token>
```

The JSON query is URL-encoded on the wire. There is no fingerprint cache, background sync, fallback
query, or directory scan.

## Public-key contract

Each authentik user may have one key in `attributes.sshPublicKey` (the attribute is configurable
via `AUTH_SERVER_KEY_ATTRIBUTE`). The value must be a
single-element JSON list containing the exact public-key string ContainerSSH supplies:

```json
{
  "sshPublicKey": ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."]
}
```

The value must exactly equal what ContainerSSH sends. During normal SSH authentication that is the
key type and base64 payload; SSH `.pub` comments are not transmitted by the client. The auth server
does not parse, normalize, or strip the value. If a caller does include a comment, it participates in
the exact match unchanged.

Authentication behavior:

- exactly one matching active user: authenticate as that authentik user;
- zero or multiple users: deny cleanly;
- authentik/network failure: return HTTP 500 so ContainerSSH fails closed.

The requested SSH username is intentionally not compared with the authentik username: in dev.box it
selects the pod template. Successful metadata still records the authentik user as the authenticated
owner.

## Endpoints

| Path | Meaning |
| --- | --- |
| `POST /pubkey` | authentik-backed SSH public-key authentication |
| `POST /password` | always denied; required only by ContainerSSH's handler interface |
| `POST /authz` | optional authentik group gate (`AUTH_SERVER_REQUIRE_GROUP`) |

The server uses ContainerSSH's official `auth/webhook` package. Pod configuration is exclusively
the separate config-server's responsibility.

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `CONTAINERSSH_LISTEN` | `0.0.0.0:8080` | Listen address |
| `CONTAINERSSH_LOG_LEVEL` | `6` | Syslog style: `7` debug through `2` critical |
| `AUTHENTIK_URL` | **required** | authentik base URL |
| `AUTHENTIK_TOKEN` / `AUTHENTIK_TOKEN_FILE` | **required** | token with read access to users; `_FILE` wins |
| `AUTHENTIK_INSECURE_SKIP_VERIFY` | `false` | skip TLS verification; development only |
| `AUTH_SERVER_KEY_ATTRIBUTE` | `sshPublicKey` | authentik user attribute the presented key is looked up by; the stored value must be a single-element JSON list containing the exact key string |
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

The chart deploys the webhook and wires ContainerSSH's public-key and authorization webhook URLs
to it. The authentik token only needs read access to users.

## Security properties

- The server is read-only against authentik.
- Authentication fails closed on API and network failures.
- Unknown, duplicate, and inactive-user keys are denied.
- Full public keys are not written to decision or error logs.
- The authenticated ContainerSSH identity is the authentik owner; the requested SSH username remains
  free to select a pod template.
