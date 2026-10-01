# Build and roll out the authentication-advertisement fix

Issue [#8](https://github.com/TU-Wien-dataLAB/dev.box/issues/8), matching upstream
[ContainerSSH#579](https://github.com/ContainerSSH/ContainerSSH/issues/579).
See [patch design and provenance](PATCH.md) for the capability contract and
[local verification](VERIFICATION.md) for results and full-suite limitations.

**Status:** locally verified, not deployed. The chart default and live release still
use stock v0.6. The stock password-prompt limitation and client workarounds still
apply there. Only a verified patched image removes the need for
`PreferredAuthentications=publickey` or `BatchMode=yes` to avoid server-password
fallback. Local encrypted-key passphrase prompts remain normal SSH client behavior.

## Build and verify

Requires Git, Docker Buildx, Python 3, Go, and OpenSSH. The image targets
`linux/amd64`, matching the pinned stock runtime. Guest images are unchanged.

```bash
# Materialize the pinned upstream source and apply the patch. Destination must not exist.
containerssh-server/prepare-source.sh /tmp/containerssh-authmethods
tests/containerssh-server-regression.sh /tmp/containerssh-authmethods

containerssh-server/build.sh containerssh-server:authmethods --load
CONTAINERSSH_IMAGE=containerssh-server:authmethods tests/chart-config-binary-validation.sh
```

The SSH tests use trusted host keys, unencrypted fixture keys, default client method
preferences, and an askpass recorder. Any unexpected prompt fails the public-key-only
test. The matrix also proves successful use of every intentionally enabled method,
including keyboard-interactive OAuth2, and preserves existing backend passthrough.
Upstream's full SSH-server suite intentionally waits 60 seconds at several shutdowns;
allow at least five minutes when running it separately. Do not run upstream `go test
./...` with your ordinary HOME: its Kubernetes fixtures use `~/.kube/config` and may
create canary pods in the current cluster. Use an isolated HOME and explicit Go cache
paths for a full-suite attempt; Docker/KinD/MinIO/guest-image fixtures are also required.

The [image workflow](../.github/workflows/containerssh-server-image.yml) publishes
`ghcr.io/tu-wien-datalab/dev.box/containerssh-server:sha-<full-dev.box-commit>` and
records the manifest digest in its job summary. It does **not** deploy. Before using
the package, make it public or configure the existing `imagePullSecrets` value.
To publish manually, use a full-commit tag with `build.sh IMAGE --push` and record
the manifest digest from its output. Never deploy a mutable branch or `latest` tag.

## Stage a server-image-only rollout

Do not run these steps during active SSH work. Restarting ContainerSSH disconnects
clients and may invalidate port-forwards; it must not delete persistent backend pods.

1. Wait for the image workflow to succeed. Save its full commit tag and manifest digest.
2. Snapshot the deployed revision, user values, manifest, and box identities:

   ```bash
   mkdir -m 700 /tmp/containerssh-image-rollout
   helm history containerssh --kube-context container-ssh -n containerssh
   helm get values containerssh --kube-context container-ssh -n containerssh -o yaml \
     > /tmp/containerssh-image-rollout/values.yaml
   helm get manifest containerssh --kube-context container-ssh -n containerssh \
     > /tmp/containerssh-image-rollout/before.yaml
   kubectl --context container-ssh -n containerssh-sessions get pods -o json \
     > /tmp/containerssh-image-rollout/pods-before.json
   ```

   Keep snapshots private: release values/manifests may contain secrets. Record the
   current revision for rollback. Do not substitute fresh example/default values.
3. Render the release using the **same chart version as the deployed release**, saved
   values, and only these overrides (replace the placeholders):

   ```bash
   helm template containerssh charts/containerssh -n containerssh --is-upgrade \
     -f /tmp/containerssh-image-rollout/values.yaml \
     --set-string image.repository=ghcr.io/tu-wien-datalab/dev.box/containerssh-server \
     --set-string 'image.tag=sha-<full-commit>@sha256:<manifest-digest>' \
     > /tmp/containerssh-image-rollout/after.yaml
   diff -u /tmp/containerssh-image-rollout/{before,after}.yaml
   ```

   Stop if anything other than the server image changes. Confirm host-key and authentik
   Secret references, auth/config webhook image tags, templates, Kata settings, RBAC,
   and network policy are unchanged. If the checkout chart differs from the deployed
   chart, use the matching chart artifact instead. The existing repository/tag values
   accept `repository:tag@sha256:digest`; no new chart option is needed.
4. After reviewing the diff, upgrade with those same values/image overrides:

   ```bash
   helm upgrade containerssh charts/containerssh --kube-context container-ssh \
     -n containerssh -f /tmp/containerssh-image-rollout/values.yaml \
     --set-string image.repository=ghcr.io/tu-wien-datalab/dev.box/containerssh-server \
     --set-string 'image.tag=sha-<full-commit>@sha256:<manifest-digest>' \
     --atomic --wait --timeout 5m
   kubectl --context container-ssh -n containerssh rollout status deployment/containerssh
   ```

## Verify without hiding password fallback

Re-establish the local port-forward if needed. Use an enrolled unencrypted key or an
already-unlocked agent key, a trusted host key, and a fresh unenrolled key. Do not
change authentik records or disable client authentication methods to pass this test.

```bash
ssh-keygen -q -t ed25519 -N '' -f /tmp/containerssh-image-rollout/unenrolled
python3 tests/ssh-auth-advertisement.py --key ~/.ssh/ENROLLED_KEY \
  --unenrolled-key /tmp/containerssh-image-rollout/unenrolled
kubectl --context container-ssh -n containerssh-sessions get pods -o json \
  > /tmp/containerssh-image-rollout/pods-after.json
```

Require successful known-template connect/reconnect, only `publickey` advertised,
exit 255 + `Permission denied (publickey)` for unknown-template/unenrolled-key denials,
no askpass invocation, and denial within five seconds. Compare pod names and UIDs with
the snapshot: existing boxes must be unchanged and denials must create no pods. The
positive control may create a box if it did not already exist; prefer an existing box.
Check auth/config logs for the negative username: the template authz gate should deny
before the config webhook, with no config retries. Existing auth-server tests cover
inactive/duplicate owners and group policy; do not mutate live users to test these.

## Roll back

On readiness or acceptance failure, restore the recorded pre-upgrade revision:

```bash
helm rollback containerssh PREVIOUS_REVISION --kube-context container-ssh \
  -n containerssh --wait --timeout 5m
kubectl --context container-ssh -n containerssh rollout status deployment/containerssh
```

Restore the port-forward and repeat the known-template positive control. Recheck box
UIDs. Rollback must not delete or recreate boxes. Stock v0.6 will again need the documented
public-key-only client workaround to prevent impossible server-password fallback.
