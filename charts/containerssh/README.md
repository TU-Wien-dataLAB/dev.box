# Helm Chart for ContainerSSH with the Kubernetes Backend

Deploys [ContainerSSH](https://containerssh.io/) with its Kubernetes backend. The chart supports
ContainerSSH's connection, session, and persistent pod lifecycles. The **dev.box deployment target
is persistent mode**: each authenticated user reuses a stable pod across SSH disconnects.

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

## Persistent mode (dev.box target)

ContainerSSH v0.6 persistent mode looks up an exact pod by
`kubernetes.pod.metadata.name`, executes SSH channels in that pod, and skips pod removal when the
SSH connection closes. With `createMissingPods: true`, it creates the named pod on first use; with
that setting false, the pod must already exist.

The intended dev.box lifecycle is:

1. derive one deterministic, collision-resistant DNS-1123 pod name from the canonical authenticated
   user (`authenticatedUsername`);
2. use `mode: persistent` and `createMissingPods: true` to create it on demand;
3. reuse that same pod name and pod across reconnects; and
4. remove it only through an explicit administrative lifecycle/cleanup policy.

This target is **not fully implemented in the chart yet**. The values path still defaults to
`connection`, force-renders `metadata.generateName`, does not expose `createMissingPods`, and the
bundled config server does not yet inject a per-user `metadata.name`. Do not set
`kubernetes.mode=persistent` by itself: without the stable name and creation setting, connections
cannot get an on-demand persistent pod.

Persistent mode preserves the **pod lifecycle**, not data independently of the pod. A pod's writable
layer is still lost if the pod is deleted, evicted, or recreated; mount a PVC or another durable
store for data that must survive those events. Changes to a pod template also do not update an
already-created persistent pod; recreate or migrate that pod explicitly to apply image/spec changes.

## Install

```bash
helm install containerssh . \
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
  --set ssh.hostKey.existingSecret=containerssh-hostkey \
  --namespace containerssh
```

```bash
# (b) Or pass a key directly to the chart at install time:
openssl genrsa -out host.key 4096
helm install containerssh . --set-file ssh.hostKey.privateKey=host.key -n containerssh
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
cluster's default controller) that's an `IngressRouteTCP`. Enable it with:

```bash
helm install containerssh . \
  --set auth.password.webhook.url=<your auth server> \
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
| `kubernetes.mode` | `connection` | Chart default; dev.box target is `persistent` after the support gaps above are implemented |
| `kubernetes.pod.metadata` | `{}` | Pod metadata extended and merged with defaults |
| `kubernetes.pod.spec` | backend user-container defaults | Full pod spec (image, volumes, resources, nodeName, securityContext…) |
| `auth.*.webhook.url` | `""` | External auth webhooks; password or publicKey is required unless the bundled auth server is enabled. **v0.6 gotcha:** the YAML key is `publicKey` — `auth.pubkey` is a deprecated boolean flag and the real binary rejects a map there (`cannot unmarshal !!map into bool`) |
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
| `kubernetes.podTemplates` | `[]` | Pod templates selected by SSH username (template `name` = username) |
| `authServer.authentik.url` | `""` | authentik base URL (required when `authServer.enabled`) |
| `authServer.authentik.token` | `""` | authentik service token (chart creates a Secret) |
| `authServer.authentik.tokenSecret` | `""` | existing Secret with the token (preferred over `token`) |
| `authServer.authentik.keyAttribute` | `""` | authentik attribute the presented key is looked up by; set `sshPublicKey` to match raw key material (debug/compat; default: derived `ssh_key_fingerprint` index) |
| `authServer.syncInterval` | `""` | normalizing+fingerprint sync interval, e.g. `5m` |

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
      spec:
        containers:
          - name: shell
            image: ubuntu:22.04
            command: ["/bin/bash"]
    - name: default
      spec:
        containers:
          - name: shell
            image: containerssh/containerssh-guest-image
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
- **Fail-closed**: while `configserver.url` is set (auto when the bundled server is enabled),
  ContainerSSH *denies* connections when the config request errors — the server must be up.
- **One config per connection**: a connection always uses the single config it fetched. In
  connection mode this normally creates a pod for that connection; in the planned persistent mode,
  multiple connections for one authenticated user resolve to and exec in the same named pod.
- **Image required**: build and push `dev.box/config-server` and point `configServer.image` at it
  (no public image exists yet).

## Bundled authentik auth server (`authServer.enabled`)

SSH key auth for this setup is a lookup call only authentik's API can serve: **given a public key,
find the user**. The bundled auth server (source in `dev.box/auth-server`, design in
`auth-server/spec.md`) implements that against authentik by fingerprinting the presented key and
querying the users API for the owner (`attributes.ssh_key_fingerprint`). It talks the ContainerSSH
`auth/webhook` protocol and exposes `/pubkey` (plus `/password`, `/authz`, `/config`).

Before enabling it:
1. build/push the image (repo CI publishes it to `ghcr.io/tu-wien-datalab/dev.box/auth-server`),
2. create an authentik **service account token** with read access to users, and
3. make sure users have `attributes.ssh_public_key` set (self-service Prompt in the authentik
   settings flow, or a provisioner) — new keys need a sync/fingerprint pass (see `authServer.syncInterval`).

Example `values.yaml`:

```yaml
authServer:
  enabled: true
  authentik:
    url: https://authentik.example.com
    tokenSecret: authentik-service-token   # existing Secret, key "token"
  syncInterval: 5m          # fingerprint free-form keys in the background
  requireGroup: "ssh-users" # optional post-auth group gate
```

When enabled the chart: creates a Secret (from `token`, or reuses `tokenSecret`), deploys the
server next to ContainerSSH, and auto-wires `auth.password/pubkey/authz.webhook.url` to its
Service (`http://<release>-auth-server.<ns>.svc.cluster.local:8080`). SSH in with the authentik
username whose key is enrolled:

```bash
helm install containerssh . \
  --set authServer.enabled=true \
  --set authServer.authentik.url=https://authentik.example.com \
  --set-file authServer.authentik.token=service-token
```

See `auth-server/README.md` for all env knobs and the lookup flow.

**Caveats:**
- **Fail-closed**: while an auth webhook URL is set, ContainerSSH *denies* connections when the
  auth request errors — authentik must be reachable from this pod.
- **Password stays off** on the server by default; the `AUTH_SERVER_PASSWORD_USERS` escape hatch
  (`authServer.passwordUsers`) grants logins with an unverified password — test/break-glass only.
- **Key uniqueness is your contract**: one fingerprint may only ever belong to one user; the sync
  flags (and refuses to write) duplicates.

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
