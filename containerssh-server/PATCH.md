# Authentication-advertisement patch

## Provenance

- Upstream repository: `https://github.com/ContainerSSH/ContainerSSH.git`
- Base: peeled `v0.6.0`, commit `76b43f44fce98315e7ab56c3db9d14248671a226`
  (`source.env`, checked during source preparation).
- Patch: `patches/0001-authentication-methods.patch`, a plain `git diff` including
  regression tests. Apply with `git apply`; no webhook-service fork is needed.
- Builder: Go 1.25.3, image pinned by multi-platform manifest digest in `Dockerfile`.
- Runtime: stock `containerssh/containerssh:v0.6`, pinned by digest in `Dockerfile`;
  only `/containerssh` is replaced. Module versions/checksums are unchanged.
- Image labels record the upstream revision and SHA-256 of the applied patch.
  Full-commit publication tags identify the dev.box recipe; manifest digests identify
  immutable artifacts. The build removes paths, VCS metadata, and build IDs from the
  binary. OCI timestamps/provenance are not promised to be byte-identical.

## Capability contract

`sshserver.AuthenticationMethodProvider` is an optional connection-handler interface.
It declares password, public-key, and keyboard-interactive support explicitly, without
fake authentication requests. The SSH server leaves disabled callbacks nil. GSSAPI
keeps its existing conditional server-provider contract; the authz wrapper now preserves
nil instead of wrapping a disabled GSSAPI server.

The auth integration derives capabilities from the authenticators actually constructed,
not raw YAML fields. Authorization, audit, metrics, and security overlays forward the
capability declaration. Authorization is never an advertised authentication method.

Existing/custom handlers without a declaration retain their legacy callbacks. This is
intentional compatibility, not inference from failed requests. Existing backend
passthrough modes still run after frontend success, rejection, or infrastructure error
as before. A disabled frontend authenticator did not pass through in v0.6 and still does
not: this patch does not introduce new backend-only authentication flows. Stock v0.6's
SSH-proxy backend authenticates its outbound connection with configured credentials;
it does not implement inbound password/key passthrough.

No authentication decision, retry policy, identity, template-selection rule, box-cap
logic, or pod lifecycle is changed. HTTP 200 + negative authz decisions remain intact.
Local private-key passphrases are client behavior, unaffected by server callbacks.

## Regression boundaries

- OpenSSH: a successful configured-template control followed by unknown-template and
  unenrolled-key denial, with default method preferences, trusted host keys, and an
  askpass recorder. Both active audit/metrics and authz overlays are included.
- SSH protocol: seven nonempty method configurations; observed advertisement matches
  configuration, and each enabled method completes an authenticated SSH handshake.
  Keyboard-interactive uses a controlled generic OAuth2 token endpoint.
- SSH protocol: success/failure/unavailable passthrough to an existing backend lacking
  the new declaration, proving backwards compatibility.
- Deployment boundary: the real patched binary accepts the chart's rendered/merged
  connection/persistent configurations and retains Kata RuntimeClass selection.
- Live deployment: `tests/ssh-auth-advertisement.py` adds connect/reconnect controls,
  default method preferences, prompt recording, and prompt-denial timing. Pod UID/no-new-
  pod and config-retry checks remain explicit operator checks in the rollout guide.

Upstream's license and contribution/DCO requirements still apply to any upstream PR.
No upstream PR, package publication, or live rollout is implied by a local commit.
