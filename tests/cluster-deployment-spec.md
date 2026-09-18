# Spec — Staged ContainerSSH validation on the `container-ssh` cluster

Status: proposed — ready to execute (the chart 0.2.0 + config-server persistent-mode contract that
this plan depended on is now implemented: default `mode: persistent` + `createMissingPods: true`,
with deterministic `metadata.name` (and request-scoped `createMissingPods` for mixed-mode charts)
injected from `authenticatedUsername`; see AGENTS.md). Remaining before execution: pin the
config-server image to an immutable SHA tag.

## Problem Statement

The server images have built and published successfully, but the first chart installation on the
`container-ssh` context exposed an invalid authentication configuration and left the ContainerSSH
Deployment crash-looping. Image builds alone do not prove that the chart, bundled webhooks,
production authentik integration, Kubernetes backend, persistent user-pod lifecycle, RBAC, and
NetworkPolicy work together.

Testing directly in the cluster without a staged plan risks confusing chart failures with image,
authentik, networking, or user-pod failures. It also risks exposing a production authentik token
through shell history or Helm values, mutating authentik through the optional sync, testing mutable
image tags, or disrupting unrelated cluster workloads.

## Solution

Validate the deployment in explicit, stop-on-failure stages on the `container-ssh` context. Use the
published immutable image tags, an existing Kubernetes Secret containing a read-only production
authentik service token, a dedicated authentik test user and SSH key, a stable SSH host key, and no
public ingress. The primary acceptance seam is a real SSH connection over a local port-forward:
SSH client → ContainerSSH → bundled auth/config webhooks → production authentik → persistent
per-user pod.

Each stage records observable evidence and must pass before the next stage begins. The plan removes
the current failed/pending release and starts from a clean install, then verifies workload readiness
and production authentik connectivity, exercises positive and negative authentication, confirms
username-selected pod configuration, stable pod identity, and reconnect persistence, and finally
verifies fail-closed behavior without disrupting production authentik itself.

## User Stories

1. As a maintainer, I want every cluster command to name the `container-ssh` context explicitly, so
   that testing cannot accidentally modify the current default cluster.
2. As a maintainer, I want the failed initial Helm release handled deliberately before retesting, so
   that stale resources do not obscure the result.
3. As a maintainer, I want the cluster to pull immutable SHA-tagged server images from GHCR, so that
   the artifacts under test are identifiable and reproducible.
4. As a maintainer, I want production authentik accessed with a read-only service token stored in an
   existing Kubernetes Secret, so that the token is absent from command history and Helm values.
5. As a maintainer, I want the authentik normalizing/fingerprint sync disabled during validation, so
   that the test cannot PATCH production user records.
6. As a maintainer, I want a dedicated authentik test user with a known SSH key and fingerprint, so
   that positive and negative authentication results are deterministic.
7. As a maintainer, I want username enforcement left enabled, so that a key cannot authenticate as a
   different SSH username.
8. As a maintainer, I want password auth to remain denied by leaving the test allowlist empty, so
   that validation preserves the key-first security posture.
9. As a maintainer, I want a stable host key mounted from a Secret, so that the SSH server has a
   predictable identity and test retries do not create host-key churn.
10. As a maintainer, I want ingress disabled and access provided through a local port-forward, so
    that the validation does not expose a new public TCP endpoint.
11. As a maintainer, I want all three application Deployments to become Ready before connecting, so
    that startup, image pulls, Secret mounts, and probes are proven independently.
12. As a maintainer, I want logs checked for `CORE_CONFIG_ERROR`, webhook startup failures, and
    authentik connectivity failures, so that readiness does not hide degraded behavior.
13. As a maintainer, I want an enrolled key with its matching authentik username to establish a real
    SSH session, so that the entire production authentication path is proven.
14. As a maintainer, I want an unknown key, a username mismatch, and password authentication to be
    denied cleanly, so that authentication failures do not become accidental access or HTTP 500s.
15. As a maintainer, I want the dedicated username to select a pod-template marker and a stable,
    collision-resistant DNS-1123 `metadata.name` merged over the base user pod, so that config
    selection, identity mapping, and ContainerSSH merge behavior are proven without replacing the
    working guest image.
16. As a maintainer, I want the persistent user pod to run as non-root with the configured resource
    limits, so that the chart's backend-pod security defaults are effective.
17. As a maintainer, I want the persistent user pod to resolve DNS and reach the public internet
    while being unable to initiate traffic to private/cluster ranges, so that the NetworkPolicy
    behaves as documented.
18. As a maintainer, I want the first connection to create the named user pod and a later connection
    to reuse the same pod UID after disconnect, so that persistent mode is proven rather than merely
    rendered.
19. As a maintainer, I want new connections denied while either bundled webhook has no Ready
    endpoints, so that auth-server and config-server failures are confirmed fail-closed without
    disrupting production authentik.
20. As a maintainer, I want the test to restore scaled workloads and leave either a known-good dev
    deployment or a fully cleaned release, so that cluster state is explicit at the end.
21. As a maintainer, I want captured image digests, Helm values excluding secrets, workload status,
    relevant logs, SSH results, and persistent user-pod observations, so that the result can be
    reviewed and repeated.

## Implementation Decisions

- **Target isolation:** every Helm and Kubernetes operation explicitly selects the `container-ssh`
  context and the chart release namespace. The current/default context is never relied upon.
- **Primary seam:** acceptance is observed through a real SSH client connection. This crosses the
  deployed ContainerSSH server, both bundled webhook services, production authentik lookup, the
  Kubernetes backend, and the persistent user pod. Direct HTTP probes are diagnostic only.
- **No in-cluster build:** the cluster pulls the already-published server images from GHCR. Both
  bundled images are pinned to immutable SHA tags for the validation run; mutable `main`/`latest`
  tags are not acceptance evidence.
- **Production authentik safety:** use a dedicated read-only service account token from an existing
  Kubernetes Secret. Do not pass token content as an inline chart value. Keep sync interval empty,
  do not mount a write token, and perform no PATCH operations.
- **Dedicated identity:** use one dedicated authentik test user whose username, canonical public key,
  and fingerprint attribute are known before deployment. The config server maps the canonical
  authenticated identity (`authenticatedUsername`) to one deterministic, collision-resistant
  DNS-1123 pod name.
- **Persistent lifecycle:** render default `mode: persistent` + `createMissingPods: true`; the
  config webhook response injects the deterministic name and also supplies `createMissingPods`
  when mixed-mode templates require it to be request-scoped. The first connection creates the
  named pod, disconnect leaves it running, and subsequent connections exec in that same pod.
  `generateName` is not accepted as a substitute for `metadata.name`.
- **Template marker:** the username-specific template changes pod metadata only (for example, a
  deterministic label plus the stable name). The base guest container/spec remains intact through
  ContainerSSH's merge behavior, avoiding an image whose default process exits before SSH can
  attach.
- **SSH exposure:** use the chart's ClusterIP Service and a local port-forward. Traefik TCP ingress
  remains disabled.
- **Host identity:** create or reuse a stable SSH host-key Secret before installation and verify the
  observed host fingerprint against it.
- **Release lifecycle:** uninstall the current `pending-upgrade` smoke-test release before creating
  test Secrets or starting acceptance. Perform a clean install with wait, timeout, and atomic
  rollback behavior so a failed stage does not leave another half-applied release.
- **Ordered gates:** preflight → install → workload readiness → first positive SSH → negative auth →
  template/security/network checks → disconnect/reconnect persistence → fail-closed checks →
  explicit cleanup. Stop after any failed gate and preserve diagnostics before rollback.
- **Fail-closed checks:** interrupt only the bundled webhook Deployments in this dev cluster, one at
  a time. Never interrupt production authentik. Restore and wait for readiness after each check.
- **End state:** choose and record one of two outcomes before running: retain a known-good deployment
  and its persistent test-user pod for continued work, or explicitly delete the persistent pod and
  any test PVCs before uninstalling the release and removing test-only Secrets. Helm uninstall does
  not own or clean up persistent backend pods.

## Testing Decisions

- **What makes a good test:** assert externally observable behavior through SSH and Kubernetes API
  observations, not template conditionals or application internals. Use exact identities, image
  digests, labels, exit statuses, and resource lifecycle transitions as evidence.
- **Main positive case:** the dedicated key and matching username establish an SSH session; the
  resulting named pod carries the username-template marker, uses the base guest container, and runs
  non-root. It remains Running after disconnect, and a second SSH connection reaches the same pod
  name and UID.
- **Negative cases:** unknown key denied, enrolled key with wrong username denied, password denied,
  auth-server unavailable denied, and config-server unavailable denied. Failures must not create a
  user pod.
- **Workload checks:** ContainerSSH, auth-server, and config-server Deployments are Ready; Services
  have Ready EndpointSlices; logs contain no critical config/startup errors; deployed image IDs
  match the intended immutable artifacts.
- **Policy checks:** DNS/public egress succeeds from the persistent user pod; representative private
  or cluster destinations are unreachable; no ingress path to the user pod is exposed.
- **Prior art:** the chart's Helm connection test checks SSH-port reachability; the server unit tests
  define auth allow/deny behavior; the webhook-contract spec defines canonical HTTP cases; the
  repository runbook documents real-binary config validation and the port-forward SSH workflow.

## Out of Scope

- Publishing or building images inside the cluster.
- Traefik `IngressRouteTCP`, LoadBalancer exposure, DNS, or public SSH traffic.
- Password verification against authentik; the bundled server deliberately does not implement it.
- Enabling the unverified password allowlist.
- Authentik sync writes, write tokens, user PATCH operations, or bulk enrollment.
- TLS/mTLS between ContainerSSH and the bundled webhook Services.
- Load, performance, soak, multi-replica, or concurrent-connection testing.
- Connection/session backend modes; this plan validates the dev.box target, persistent mode.
- Mutating or deliberately disrupting the production authentik deployment.

## Further Notes

- The first cluster attempt already proved that the public config-server image pulls and reaches
  Ready. It also produced the chart-auth failure that must be fixed before this plan runs.
- The existing release in the `containerssh` namespace is currently `pending-upgrade` after an
  aborted upgrade. Both workloads are Ready, but ContainerSSH is using the omission-only auth draft
  and has no usable configured authenticator; uninstall it as the first execution step.
- The normal chart/config-server path now renders the persistent-mode contract (chart 0.2.0):
  default base `mode: persistent` + `createMissingPods: true`, with deterministic `metadata.name`,
  request-scoped `createMissingPods` where mixed modes require it, and the `dev.box/owner` label
  injected from the canonical authenticated identity (`authenticatedUsername`), plus the
  per-owner cap. Both the config-server image and this plan's steps must still be pinned/
executed as described above.
- Production authentik remains an external dependency. Before execution, record its base URL, the
  existing read-token Secret name/key, the dedicated test username, and the matching private-key
  location without committing secret values.
- This plan is separate from the local Docker-compose webhook contract framework: the contract suite
  is fast and deterministic, while this plan validates the real cluster and production authentik
  boundary.
