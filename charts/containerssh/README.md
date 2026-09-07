# Helm Chart for ContainerSSH with the Kubernetes Backend

Deploys [ContainerSSH](https://containerssh.io/) so that every SSH user is dropped into their own
ephemeral Kubernetes pod. Pods are created on demand and cleaned up when the user disconnects.

This chart is modeled on the official documentation:

- Installation in Kubernetes: https://containerssh.io/v0.6/getting-started/installation/ (Kubernetes tab)
- Kubernetes backend reference: https://containerssh.io/v0.6/reference/kubernetes/

## Design (what the chart creates)

| Resource | Purpose |
| --- | --- |
| `Deployment` | Runs the `containerssh/containerssh` SSH server (default port **2222**) |
| `ConfigMap` | Renders the ContainerSSH `config.yaml` (Kubernetes backend) from `values.yaml` |
| `ServiceAccount` | Identity used *by ContainerSSH* to talk to the Kubernetes API (bearer token + in-cluster CA) |
| `Role` + `RoleBinding` | Least-privilege RBAC that lets the ServiceAccount create/exec/log session pods **only** in the session namespace (see the "Securing Kubernetes" section of the reference) |
| `Namespace` *(optional)* | The session namespace where per-session pods run |
| `NetworkPolicy` *(optional)* | Session pods: no ingress, egress to the public internet only |
| `Secret` *(optional)* | Stable SSH host key (see below) |
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
- An authentication server reachable from the cluster (the chart requires one configured auth
  method; by default it uses a password webhook — set `auth.password.webhook.url`).

> **Session pods vs. ContainerSSH pods**: by default the per-SSH-session pods are launched in a
> **separate** namespace (`containerssh-sessions`) so that ContainerSSH itself does not share a
> namespace with the untrusted pods it spawns, as recommended in the reference.

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
| `kubernetes.sessionNamespace` | `containerssh-sessions` | Namespace where per-session pods are created |
| `kubernetes.createSessionNamespace` | `true` | Create that namespace if it doesn't exist |
| `kubernetes.mode` | `connection` | `connection` / `session` / `persistent` (see reference) |
| `kubernetes.pod.metadata` | `{}` | Pod metadata extended and merged with defaults |
| `kubernetes.pod.spec` | session container defaults | Full pod spec (image, volumes, resources, nodeName, securityContext…) |
| `auth` | password webhook | Auth backend config, rendered as-is |
| `service.type`, `service.port` | `ClusterIP`, `2222` | SSH service type/port |
| `ingress.enabled` | `false` | Expose SSH via a Traefik `IngressRouteTCP` (raw TCP) |
| `ingress.tcp.entryPoint` | `ssh` | Traefik static TCP entrypoint (LB port) to route from |
| `ingress.tcp.servicePort` | `2222` | Chart Service port the route targets (== `service.port`) |
| `rbac.enabled` | `true` | Create the session-pod Role/RoleBinding |
| `networkPolicy.enabled` | `true` | NetworkPolicy on the session namespace |
| `networkPolicy.allowClusterDNS` | `true` | Allow DNS to kube-dns so internet egress resolves names |
| `networkPolicy.exceptRanges` | RFC1918, CGNAT, link-local | CIDRs treated as "not internet" (blocked) |
| `networkPolicy.extraEgress` | `[]` | Extra egress peers for session pods |
| `configserver.url` | `""` | Config server URL (auto-set when bundled server is enabled) |
| `configServer.enabled` | `false` | Deploy the bundled config server in-chart |
| `kubernetes.podTemplates` | `[]` | Pod templates selected by SSH username (template `name` = username) |

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
- **One pod per connection**: a connection always uses the single config it fetched. Several
  templates mean different connections/users land in different pods — not several pods inside one
  connection.
- **Image required**: build and push `dev.box/config-server` and point `configServer.image` at it
  (no public image exists yet).

## Security guidance (from the reference)

The chart ships sensible defaults for the **session** pods: `runAsNonRoot`, `runAsUser: 1000`,
`allowPrivilegeEscalation: false` and CPU/memory limits. To lock down further you can, per the
reference:

- mount per-user volumes (they cannot be preconfigured globally — use the configuration server),
- apply the `readOnlyRootFilesystem` policy/PSP to the session namespace,
- if you enforce the Pod Security `restricted` profile on the session namespace, add
  `seccompProfile: {type: RuntimeDefault}` to the session pod spec (the chart does this manually), and
- the chart ships a `NetworkPolicy` on the session namespace that already implements the
  reference's "Limiting network access" example: **no ingress**, and egress to the public
  internet only. Internal ranges are blocked by default (adapt `networkPolicy.exceptRanges`
  to your pod/service CIDRs, and use `networkPolicy.extraEgress` to open specific
  cluster-internal services the session pods must reach). Disable with `networkPolicy.enabled: false`
  if your CNI does not support NetworkPolicies.

## Uninstall

```bash
helm uninstall containerssh -n containerssh
```

Note: session pods are owned by ContainerSSH, not by this chart — they are removed when their SSH
session ends. If any remain, clean up the session namespace yourself.
