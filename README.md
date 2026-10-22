# dev.box

SSH into persistent Kubernetes dev boxes, with authentik public-key authentication
and a terminal UI for managing your boxes.

## Connect

For the dataLAB deployment:

1. Add your public key to the **dev.box SSH Public Key** field in
   [authentik settings](https://login.datalab.tuwien.ac.at/if/user/#/settings).
2. Connect with an available template name: `ssh ubuntu@dev.box.datalab.tuwien.ac.at`.
3. Manage your boxes with `ssh -t settings@dev.box.datalab.tuwien.ac.at`.

The SSH username selects a template; your key identifies its owner. Reconnecting to
an ordinary template reuses the same pod. A persistent pod is **not durable storage**:
only data in explicitly configured retained PVC mounts survives pod recreation.
Settings can delete a box; it cannot delete its retained PVCs.

## Repository

| Path | Purpose |
| --- | --- |
| [`charts/containerssh/`](charts/containerssh/README.md) | Helm deployment, templates, policy and storage configuration |
| [`auth-server/`](auth-server/README.md) | Exact public-key lookup in authentik; template authorization |
| [`config-server/`](config-server/README.md) | Per-owner pod configuration, box cap, retained claims and scoped Settings access |
| [`settings-tui/`](settings-tui/README.md) | Shell-free terminal management application and guest image |
| [`tests/`](tests/README.md) | Offline regression, Docker image and opt-in live checks |

The SSH gateway is built in the maintained
[ContainerSSH fork](https://github.com/TU-Wien-dataLAB/ContainerSSH), not here.
The gateway and Settings guest are separate programs and images.

## Develop

```sh
helm dependency build charts/containerssh
./tests/check.sh
```

See [test prerequisites and image checks](tests/README.md). Image publishing
workflows do not deploy to the cluster.

## Deploy and operate

Use the [chart documentation](charts/containerssh/README.md) for installation and
configuration, and [operations](docs/operations.md) for current deployment state,
approval gates and live acceptance. Preserve immutable image pins and existing
Secrets during upgrades. Pod/PVC deletion and live rollout require explicit approval.
