# How dev.box fits together

For maintainers: this page explains the request flow and where changes belong. For commands,
start with [tests](../tests/README.md); for any real deployment work, read
[operations](operations.md) and the [recorded deployment status](deployment-status.md) first.

## Two identities, two request paths

The SSH **login name selects a template**; the SSH key identifies its **verified owner**.
For example, two owners connecting as `ubuntu` receive different boxes. `default` is an
ordinary template, not a fallback. `settings` is a reserved application, not a template file.

```text
SSH client
    │ login name + public key
    ▼
ContainerSSH gateway (separate fork)
    ├── auth-server ── exact key lookup ── authentik
    │       └── verified owner; template/group authorization
    │
    └── config-server (login name + verified owner)
            ├── ordinary template
            │     └── identity + cap + optional retained claims → box configuration
            └── settings
                  └── owner-scoped RBAC + code-owned application configuration

Gateway merges the response with its base configuration and starts/reuses the pod.
Settings pod → settings-tui → exact-name pod GET/DELETE with the owner's scoped token
```

Auth distinguishes policy denial from infrastructure failure: denial is prompt, whereas errors
fail closed and can be retried by the gateway. Config-server validates template selection again;
it does not treat successful authentication as permission to load arbitrary configuration.

## Persistent does not mean durable

An ordinary persistent box keeps the same pod across SSH disconnects. Deleting or recreating that
pod loses its writable layer and ephemeral volumes. Optional retained PVCs preserve only data
written into their mounts; they are not backups. Template edits change future configuration,
not existing pods or claims.

Settings is different: it is a disposable connection-mode application. It can inspect and delete
only the owner's exact ordinary persistent pod names, not list all pods or manage storage.
Its row list and RBAC grants come from the same template walk. The UI's box count is advisory;
config-server enforces the cap. Quitting the UI need not close a multiplexed SSH transport.

## Why these boundaries exist

- **auth-server** owns identity and early authorization, not pod construction. Authentik access
  is read-only and uses one exact key query, not a user-directory scan.
- **config-server** owns template selection, persistent identity, cap checks, retained-claim
  creation and owner access reconciliation. These decisions need trusted identity and API access.
- **settings-tui** owns terminal interaction and narrowly scoped pod operations, not SSH policy.
  The gateway and synthesized configuration deny exec, forwarding and other unsupported requests.
- **The Helm chart** wires images, configuration, RBAC and networks together. The shared base is
  deliberately restricted: ContainerSSH's merge cannot clear inherited fields with empty/false
  values, and a nonempty container slice replaces the base slice.
- **The gateway fork** owns SSH protocol and pod lifecycle fixes. Its source is maintained outside
  this repository; image approval and deployment approval are separate decisions.

## Where to change what

| Change | Start here | Contract / checks |
| --- | --- | --- |
| Key ownership, denial messages, group/template authorization | `auth-server/auth_handler.go`, `authentik.go`, `webhook.go` | [Auth README](../auth-server/README.md), adjacent Go tests |
| Template lookup and request orchestration | `config-server/handler.go` | [Config README](../config-server/README.md), handler tests |
| Persistent identity and cap | `config-server/persistent.go` | Persistent tests |
| Owner grants and Settings policy | `config-server/owner_access.go`, `settings.go` | Owner-access and Settings tests |
| Retained claims | `config-server/storage.go` | Storage tests; [chart authoring](../charts/containerssh/README.md#author-a-template-with-retained-storage) |
| Terminal state, layout, API requests | `settings-tui/model.go`, `view.go`, `kube.go` | [TUI README](../settings-tui/README.md), model and executable tests |
| Deployment inputs and rendered resources | `charts/containerssh/values.yaml`, `templates/` | [Chart README](../charts/containerssh/README.md), `tests/chart-*.sh` |
| Gateway protocol/lifecycle | Separate `TU-Wien-dataLAB/ContainerSSH` fork | Worktree and approval rules in [AGENTS.md](../AGENTS.md) |

Keep interface contracts in their owning README; link across boundaries rather than copying them.
Recorded release state belongs in [deployment status](deployment-status.md), not implementation specs.

## Verification boundaries

[The test reference](../tests/README.md) is the command source of truth:

1. `./tests/check.sh`: offline application and rendering regression checks.
2. Docker checks: packaged images and real gateway behavior, separate from the offline runner.
3. Approved live acceptance: actual SSH, projected credentials, networking and lifecycle evidence.

Real merge/backend/security validation is necessary: successful YAML rendering or gateway
`--dump-config` parsing alone does not establish policy. Historical acceptance does not transfer
to a new release, and an SSH probe can create pods or claims even when it appears observational.
