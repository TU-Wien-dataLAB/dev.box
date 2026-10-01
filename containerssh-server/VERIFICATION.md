# Local verification — 2026-10-01

No image publication or server-image rollout was performed. Live acceptance remains
an operator step in the [rollout guide](README.md).

## Passed

- Red/green: stock v0.6 triggered three askpass password prompts for both unknown-template
  and unenrolled-key denials; the patched public-key-only server triggered none. The same
  enrolled fixture key first completed an OpenSSH command successfully.
- Seven method configurations, including successful password, public-key, and generic
  OAuth2 keyboard-interactive handshakes; all advertised sets matched configuration.
- All three existing backend passthrough behaviors, with a legacy backend handler.
- Ten consecutive repetitions of the new upstream SSH tests passed after limiting the
  matrix's positive assertion to authentication (avoiding an unrelated fast-command
  reply/close race in upstream's test backend).
- `tests/containerssh-server-regression.sh`: the Python acceptance harness passed against
  a real local SSH server with known-template connect/reconnect and both denial cases.
- Upstream `go build ./...` and `go vet ./...`; full SSH-server suite (250 seconds),
  security and metrics tests. dev.box auth/config servers: full build/vet/test suites.
- All five chart rendering matrices and all five patched-binary config validation cases.
- ShellCheck, actionlint, and staged whitespace checks.
- Two independently prepared image builds yielded identical `/containerssh` SHA-256:
  `905d13d5280a451fdb8a5ffd52c4725b874e9393a853f1af7cc2276fbfa6b3ad`.
  Final local image ID: `sha256:dee6fe12b224a190b9d18ac5c4b10c29f12287d3d6ac898c9f695e035b6f9fb5`.
  This is a local config digest, **not** a published registry manifest reference.

## Full upstream suite limitations

The full upstream suite did not pass: MinIO image pulls were denied, Kerberos's UDP
port 88 could not bind, guest-image fixtures were missing, and isolated KinD/Docker
fixtures timed out. Audit-log assertions also failed on the unchanged upstream baseline.
These unrelated integration fixtures were not repaired as part of issue #8. The first
isolated full run exposed the matrix's fast-command fixture race noted above; its updated
authentication assertions subsequently passed ten consecutive runs.

The initial full-suite attempt unexpectedly read `~/.kube/config` and created a temporary
`containerssh-test-canary-*` pod in the live cluster's `default` namespace. The upstream
fixture deleted it; a read-only check confirmed no remaining canaries. Further full-suite
attempts used an isolated HOME. Test-owned local KinD containers were removed after timeouts.
The persistent session pod remained Running with its original UID:
`box-8bbfc143fa` / `b2abba32-495f-4212-a16c-b2490144789c`.

## Deferred

Publish the full-commit-tagged image, pin its registry digest, review the server-image-only
rendered diff, deliberately roll out, and verify unmasked SSH denials plus unchanged box
UIDs and no config-webhook retries. Do not treat local tests as live deployment evidence.
