# Helm Chart for ContainerSSH with the Kubernetes Backend

Deploys [ContainerSSH](https://containerssh.io/) with its Kubernetes backend. The chart supports
ContainerSSH's connection, session, and persistent pod lifecycles. The **dev.box deployment target
is persistent mode** (the chart default): each authenticated user reuses a stable pod across SSH
disconnects, with a configurable cap on how many boxes one user may keep.

This chart is modeled on the official documentation:

- Installation in Kubernetes: https://containerssh.io/v0.6/getting-started/installation/ (Kubernetes tab)
- Kubernetes backend reference: https://containerssh.io/v0.6/reference/kubernetes/

## Design (what the chart creates)

| Resource | Purpose |
| --- | --- |
| `Deployment` | Runs the `containerssh/containerssh` SSH server (default port **2222**) |
| `ConfigMap` | Renders the ContainerSSH `config.yaml` (Kubernetes backend) from `values.yaml` |
| `ServiceAccount` | Identity used *by ContainerSSH* to talk to the Kubernetes API (bearer token + in-cluster CA) |
| `Role` + `RoleBinding` | Least-privilege RBAC that lets the ServiceAccount create/exec/log backend user pods **only** in their namespace (see the "Securing Kubernetes" section of the reference) |
| `Namespace` *(optional)* | The isolated namespace where backend user pods run |
| `NetworkPolicy` *(optional)* | Backend user pods: no ingress, egress to the public internet only |
| `Secret` *(optional)* | Stable SSH host key (see below) |
| `Deployment` + `Service` *(optional)* | Bundled authentik-backed auth server (`authServer.enabled`) |
| `Deployment` + `Service` + `ServiceAccount` + `Role`/`RoleBinding` *(optional)* | Bundled config server (`configServer.enabled`) — its own ServiceAccount holds **list-only** permission on pods in the session namespace, just enough for the persistent-mode box cap |
| `Service` | Exposes the SSH port (ClusterIP by default) |
| `test-connection` Job | `helm test` smoke check that the SSH port is reachable |

The Kubernetes backend configuration follows the reference:

```yaml
backend: kubernetes
kubernetes:
  connection:
    host: kubernetes.default.svc
    cacertFile: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
    bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
  pod:
    metadata:
      namespace: <session-namespace>
      generateName: containerssh-
    spec:
      containers:
        - name: shell
          image: containerssh/containerssh-guest-image
```

## Prerequisites

- A Kubernetes cluster and a kube context (the chart assumes the deployment runs *inside* the
  cluster it manages; `kubernetes.connection.host=kubernetes.default.svc` only resolves in-cluster).
- An authentication server reachable from the cluster. Chart rendering fails unless the bundled
  `authServer` is enabled or `auth.password.webhook.url` / `auth.publicKey.webhook.url` is set;
  authorization alone is not an authentication method. For public-key auth against authentik,
  enable the bundled `authServer` (below).

> **User pods vs. ContainerSSH pods**: backend user pods run in a **separate** namespace
> (`containerssh-sessions`) so that ContainerSSH itself does not share a namespace with the
> untrusted pods it accesses or spawns, as recommended in the reference.

## Persistent mode (dev.box target — the chart default)

ContainerSSH v0.6 persistent mode looks up an exact pod by `kubernetes.pod.metadata.name`,
executes SSH channels in that pod, and skips pod removal when the SSH connection closes. The chart
default is `kubernetes.mode: persistent` with `createMissingPods: true`; the config server supplies
the request-specific `metadata.name` and also sets `createMissingPods: true`, so the named pod is
created on first use and reused afterwards. If any chart-managed template explicitly selects
`connection` or `session`, the chart omits base-level `createMissingPods` and leaves it to each
persistent response: ContainerSSH's merge cannot clear a true boolean, so an unconditional base
true would make that non-persistent template invalid.

The box identity is provided by the **config server** at connection time:

1. the bundled config server derives a deterministic, collision-resistant DNS-1123 pod name from
   the canonical authenticated user (`authenticatedUsername`) **plus** the resolved pod template,
   and returns it with `createMissingPods: true` (merged over the base config);
2. it labels every box `dev.box/owner` = the authenticated user (audit + cap counting);
3. it enforces the per-user box cap (`configServer.maxPodsPerUser`, default 3): the first
   connection creates the box, reconnects exec into the same pod, opening additional boxes happens
   by using usernames that resolve to *different* templates, and spawning a new box while at the
   cap is denied server-side (reconnecting to an existing box always passes).

Because the box identity comes from the *authenticated* username, two users who both type
`ssh ubuntu@…` never share a pod, and unknown usernames collapse onto one shared `default` box per
owner instead of minting unbounded pods. The cap and naming logic live in the config webhook, so
clients cannot bypass them.

**Requirements / caveats:**

- Persistent mode needs a config server to inject the deterministic per-user pod name (and
  request-scoped `createMissingPods: true` for mixed-mode templates) — enable the bundled
  `configServer` or run this repository's config server behind an external `configserver.url` with
  its persistent-mode environment configured. The chart **fails rendering** if
  `kubernetes.mode: persistent` is set without one. Use `kubernetes.mode: connection` for classic
  per-connection behavior without a config server.
- Persistent mode preserves the **pod lifecycle**, not data independently of the pod. A pod's
  writable layer is still lost if the pod is deleted, evicted, or recreated; mount a PVC or another
  durable store for data that must survive those events.
- Pods are **never deleted automatically**, by the chart or the config server. Bin the cap, then
  delete an operator-identified box explicitly (`kubectl delete pod <name> -n
  $SESSION_NAMESPACE`, owner label `dev.box/owner=<user>`).
- Changes to a pod template do not update an already-created persistent pod; because the pod name
  is deterministic, operators can recreate/migrate the named pod deliberately to apply changes.
- The chart adds a checksum of its generated `config.yaml` to the main Deployment, so Helm config
  changes restart ContainerSSH and load the new mode/settings. If `existingConfigMap` is used, Helm
  cannot checksum that external object; restart the Deployment after changing it.

## Install

The chart default is **persistent mode**, which needs a config server (bundled or external) to
inject stable per-user pod names — see [Persistent mode](#persistent-mode-devbox-target---the-chart-default).
The minimal examples below pin `kubernetes.mode=connection` so they need only an auth server; add
`--set configServer.enabled=true` (and a built config-server image) for the persistent default.

```bash
helm install containerssh . \
  --set kubernetes.mode=connection \
  --set auth.password.webhook.url=https://auth.example.com/ \
  --namespace containerssh \
  --create-namespace
```

### SSH host keys

If no host key is provided, ContainerSSH generates an **ephemeral** key at startup (logged as a
warning) — the fingerprint changes on every pod restart. For a stable key, do one of:

```bash
# (a) Create a Secret up front, exactly like the installation docs:
openssl genrsa | kubectl create secret generic containerssh-hostkey \
  --from-file=host.key=/dev/stdin -n containerssh

helm install containerssh . \
  --set kubernetes.mode=connection \
  --set ssh.hostKey.existingSecret=containerssh-hostkey \
  --namespace containerssh
```

```bash
# (b) Or pass a key directly to the chart at install time:
openssl genrsa -out host.key 4096
helm install containerssh . --set kubernetes.mode=connection --set-file ssh.hostKey.privateKey=host.key -n containerssh
```

### Connect

```bash
# port-forward to the SSH service, then:
ssh -p 2222 some-user@localhost
```

For a real (ClusterIP) service you can also expose it with `service.type=NodePort` or
`service.type=LoadBalancer` and `ssh -p <nodePort|loadBalancerPort> user@<node|lb-ip>`.

### Expose via the Traefik ingress (raw TCP) — no cert-manager

ContainerSSH is an **SSH server on a TCP port**, so an HTTP(S) Ingress cannot carry it:
HTTP Ingresses route HTTP, and cert-manager/ACME issues X.509 certificates only for
HTTPS. SSH encrypts and authenticates its own transport (its identity is the SSH host
key, which this chart auto-generates — see `ssh.hostKey`), so there is no TLS/cert to
set up for SSH at all.

The way to front SSH with an ingress is a **raw-TCP route**. With Traefik (this
cluster's default controller) that's an `IngressRouteTCP`. Enable it with (a persistent-mode
example: auth server + bundled config server):

```bash
helm install containerssh . \
  --set auth.password.webhook.url=<your auth server> \
  --set configServer.enabled=true \
  --set ingress.enabled=true \
  --namespace containerssh
```

One-time cluster prerequisite (Traefik only listens on the entrypoints/ports you
give it, so the LB must publish a TCP port for SSH):

```bash
kubectl -n traefik patch svc traefik --type=json -p \
  '[{"op":"add","path":"/spec/ports/-","value":{"name":"ssh","port":2222,"targetPort":2222,"protocol":"TCP"}}]'
kubectl -n traefik patch deploy traefik --type=json -p \
  '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--entryPoints.ssh.address=:2222/tcp"}]'
```

Then `ssh -p 2222 ubuntu@<traefik-host>` drops you into the "ubuntu" pod template
(the SSH login name selects the pod template: `ubuntu` → the `ubuntu` podTemplate;
no matching template falls back to `default`/the base pod). Tune the entrypoint and
service port via `ingress.tcp.*` (see `values.yaml`).

## Configuration

See `values.yaml` for the complete, annotated list. Highlights:

| Parameter | Default | Description |
| --- | --- | --- |
| `image.repository`, `image.tag` | `containerssh/containerssh`, `v0.6` | ContainerSSH image |
| `replicaCount` | `1` | Number of SSH server replicas |
| `ssh.port` | `2222` | SSH listener port |
| `ssh.hostKey.existingSecret` | `""` | Name of a Secret holding `host.key` (recommended) |
| `ssh.hostKey.privateKey` | `""` | Inline PEM host key (chart creates the Secret from it) |
| `kubernetes.sessionNamespace` | `containerssh-sessions` | Namespace where backend user pods run |
| `kubernetes.createSessionNamespace` | `true` | Create that namespace if it doesn't exist |
| `kubernetes.mode` | `persistent` | `connection`, `session`, or `persistent` (dev.box default). `persistent` needs a config server (bundled or external) — the chart fails otherwise |
| `kubernetes.pod.metadata` | `{}` | Pod metadata extended and merged with defaults |
| `kubernetes.pod.spec` | backend user-container defaults | Full pod spec (image, volumes, resources, nodeName, securityContext…) |
| `auth.*.webhook.url` | `""` | External auth webhooks; password or publicKey is required unless the bundled auth server is enabled. **v0.6 gotcha:** the YAML key is `publicKey` — `auth.pubkey` is a deprecated boolean flag and the real binary rejects a map there (`cannot unmarshal !!map into bool`) |
| `auth.*.webhook.timeout` | `30s` | Per-request timeout, kept below the 60s overall authentication timeout |
| `authServer.enabled` | `false` | Deploy the bundled authentik-backed auth server and wire it |
| `service.type`, `service.port` | `ClusterIP`, `2222` | SSH service type/port |
| `ingress.enabled` | `false` | Expose SSH via a Traefik `IngressRouteTCP` (raw TCP) |
| `ingress.tcp.entryPoint` | `ssh` | Traefik static TCP entrypoint (LB port) to route from |
| `ingress.tcp.servicePort` | `2222` | Chart Service port the route targets (== `service.port`) |
| `rbac.enabled` | `true` | Create the backend-pod Role/RoleBinding |
| `networkPolicy.enabled` | `true` | NetworkPolicy on the session namespace |
| `networkPolicy.allowClusterDNS` | `true` | Allow DNS to kube-dns so internet egress resolves names |
| `networkPolicy.exceptRanges` | RFC1918, CGNAT, link-local | CIDRs treated as "not internet" (blocked) |
| `networkPolicy.extraEgress` | `[]` | Extra egress peers for backend user pods |
| `configserver.url` | `""` | Config server URL (auto-set when bundled server is enabled) |
| `configServer.enabled` | `false` | Deploy the bundled config server in-chart |
| `configServer.maxPodsPerUser` | `3` | Per-owner live-box cap in persistent mode (0 = disabled); always allows reconnects |
| `kubernetes.podTemplates` | `[]` | Pod templates selected by SSH username (template `name` = username) |
| `authServer.authentik.url` | `""` | authentik base URL (required when `authServer.enabled`) |
| `authServer.authentik.token` | `""` | authentik service token (chart creates a Secret) |
| `authServer.authentik.tokenSecret` | `""` | existing Secret with the read-only token (preferred over `token`) |

## Per-username pod templates

`ssh <user>@host` can drop that user into a pod template named `<user>`. ContainerSSH uses exactly
ONE pod config per SSH connection, so bringing several named pod templates needs a small helper:
the **bundled config server** (source in `dev.box/config-server`).

1. You define a list of pod templates under `kubernetes.podTemplates` in `values.yaml`.
2. Enable the bundled server (`configServer.enabled=true`); the chart feeds it the templates via a
   mounted ConfigMap and auto-wires `configserver.url` to the in-chart Service.
3. On every SSH connection, ContainerSSH asks the config server for a config, which is **merged
   over the base config**. The server looks up a template **by the SSH username**:
   `ssh ubuntu@host` → template named `ubuntu`; no match → template named `default`; still none →
   base config.

Net effect: each user gets their own pod flavor by simply connecting as that user name. A
`default` template acts as the catch-all for everyone else; empty list → the base `kubernetes.pod`
applies to everyone.

Example `values.yaml`:

```yaml
kubernetes:
  pod:
    # default/fallback pod (mirrors the templates' structure)
    spec:
      containers:
        - name: shell
          image: containerssh/containerssh-guest-image
  podTemplates:
    - name: ubuntu
      metadata:
        labels:
          dev-box-template: ubuntu
    - name: default
      metadata:
        labels:
          dev-box-template: default
```

Templates are **partial** `kubernetes.pod` overrides — unset fields (mode, metadata, spec, limits,
...) are inherited from the base config thanks to ContainerSSH's deep merge (`structutils.Merge`;
verified: overriding only labels preserves the base containers/spec). The server looks up the file
`<username>.yaml` (sanitized), then `default.yaml`; selection is deterministic per user, so the
server stays stateless and replica-safe (no coordination needed).

Wire it up:

```bash
helm install containerssh . \
  --set auth.password.webhook.url=<your auth server> \
  --set configServer.enabled=true
```

**Caveats:**
- **Guest image contract**: keep metadata-only templates unless the replacement image contains
  `/usr/bin/containerssh-agent`. A plain image such as `ubuntu:22.04` inherits the agent command
  from the base pod but fails at startup because the binary is absent.
- **Fail-closed**: while `configserver.url` is set (auto when the bundled server is enabled),
  ContainerSSH *denies* connections when the config request errors — the server must be up.
- **Persistent boxes (default)**: in persistent mode each authenticated user keeps ONE stable box
  per template, capped at `configServer.maxPodsPerUser` (default 3) — same username + same
  template always resolves to the same pod; more templates = more boxes up to the cap; spawning at
  the cap is denied while reconnecting always works. Delete a box explicitly when it is no longer
  needed (see the persistent-mode section above).
- **Image required**: build and push `dev.box/config-server` and point `configServer.image` at it
  (no public image exists yet).

## Bundled authentik auth server (`authServer.enabled`)

SSH key auth asks authentik one question: **which user has this exact public key?** The bundled
server performs one exact users query for the string supplied by ContainerSSH against
`attributes.sshPublicKey`. It exposes ContainerSSH's `/pubkey` webhook plus the interface-required
`/password` (always denied) and `/authz` (optional authentik group gate) endpoints. Pod
configuration is the separate bundled config server's job — this server never serves `/config`.

Before enabling it:
1. build/push the image (repo CI publishes it to `ghcr.io/tu-wien-datalab/dev.box/auth-server`),
2. create an authentik service-account token with read access to users, and
3. store each user's key as a single-element `attributes.sshPublicKey` list whose item exactly
   matches ContainerSSH's webhook value. Normal SSH authentication supplies `type + base64` without
   the optional `.pub` comment.

Example `values.yaml`:

```yaml
authServer:
  enabled: true
  authentik:
    url: https://authentik.example.com
    tokenSecret: authentik-service-token   # existing Secret, key "token"
  requireGroup: "ssh-users"                # optional post-auth group gate
```

When enabled the chart: creates a Secret (from `token`, or reuses `tokenSecret`), deploys the
server next to ContainerSSH, and auto-wires `auth.publicKey/authz.webhook.url` to its Service
(`http://<release>-auth-server.<ns>.svc.cluster.local:8080`). Authenticate with an enrolled key;
the requested SSH username selects the pod template, while authenticated metadata records the
key's authentik owner:

```bash
helm install containerssh . \
  --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.com \
  --set configServer.enabled=true \
  --set-file authServer.authentik.token=service-token
```

See `auth-server/README.md` for all env knobs and the lookup flow.

**Caveats:**
- **Fail-closed**: while an auth webhook URL is set, ContainerSSH *denies* connections when the
  auth request errors — authentik must be reachable from this pod.
- **Password is not configured** by the bundled server, and its protocol-required `/password`
  handler always denies.
- **Public-key equality is strict**: `attributes.sshPublicKey` must be a single-element list whose
  item exactly matches the webhook value. Zero or multiple matches deny cleanly; there is no
  alternate query or directory scan.

## Security guidance (from the reference)

The chart ships sensible defaults for the **session** pods: `runAsNonRoot`, `runAsUser: 1000`,
`allowPrivilegeEscalation: false` and CPU/memory limits. To lock down further you can, per the
reference:

- mount per-user volumes (they cannot be preconfigured globally — use the configuration server),
- apply the `readOnlyRootFilesystem` policy/PSP to the session namespace,
- if you enforce the Pod Security `restricted` profile on the session namespace, add
  `seccompProfile: {type: RuntimeDefault}` to the backend pod spec (the chart does this manually), and
- the chart ships a `NetworkPolicy` on the session namespace that already implements the
  reference's "Limiting network access" example: **no ingress**, and egress to the public
  internet only. Internal ranges are blocked by default (adapt `networkPolicy.exceptRanges`
  to your pod/service CIDRs, and use `networkPolicy.extraEgress` to open specific
  cluster-internal services the backend user pods must reach). Disable with `networkPolicy.enabled: false`
  if your CNI does not support NetworkPolicies.

## Uninstall

```bash
helm uninstall containerssh -n containerssh
```

Backend pods are not owned by the Helm release. ContainerSSH removes them after SSH disconnect in
connection/session modes, but deliberately retains them in persistent mode. Before uninstalling a
persistent deployment, decide whether to retain or explicitly delete its user pods and PVCs.
