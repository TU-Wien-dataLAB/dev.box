# Settings TUI POC

Standalone Go + Bubble Tea application for issue #9, not an SSH server (no Wish).
It displays only Settings, POC/read-only, the authenticated owner, and `q: quit`.

## Build, run, and check locally

From the repository root, with Go 1.26+ and a Unix terminal:

```bash
(cd settings-tui && go build -o /tmp/dev-box-settings . && go vet ./... && go test ./...)
DEV_BOX_AUTHENTICATED_OWNER=demo.owner /tmp/dev-box-settings

docker build --platform linux/amd64 -t dev-box-settings:local settings-tui
docker run --rm --platform linux/amd64 -it \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -e DEV_BOX_AUTHENTICATED_OWNER=demo.owner dev-box-settings:local
python3 tests/settings-image-smoke.py --image dev-box-settings:local
```

The Go tests launch a separately built executable through real PTYs. The Python
smoke check requires only the standard library and Docker; it creates and removes
its own local containers. It checks rendering, resize, quit/terminal restoration,
inert command/suspend/escape input, identity sanitization, startup failures,
shell absence, and agent console/idle-signal compatibility.

## Execution contract

- Executable: **`/usr/local/bin/dev-box-settings`**, launched directly without a
  shell wrapper. The Docker entrypoint is this executable.
- Identity: **`DEV_BOX_AUTHENTICATED_OWNER`** must contain the nonempty canonical
  owner supplied by trusted gateway/config-server plumbing. Unset, empty, and
  whitespace-only values fail closed with exit 1. No username, image-user, or
  original-command fallback exists. Local examples use a synthetic identity.
- Terminal: stdin and stdout must both be terminals with nonzero initial output
  dimensions. Initial dimensions may arrive after the agent PID handshake;
  startup waits up to two seconds for a zero-sized PTY to become usable.
  Otherwise it exits 1 with a helpful `ssh -t` hint, without opening a fallback
  `/dev/tty`. Non-PTY use fails immediately; checks precede Bubble Tea execution.
- UI: lowercase, unmodified `q` is the only action and exits 0, restoring terminal
  state. Other input, including commands, Ctrl-Z, Ctrl-C, and ESC, is ignored;
  no subprocess, shell, editor, pager, data fetching, or management action runs.
- Display: printable Unicode owner text is preserved; controls, format/bidi
  characters, and nonprinting separators become `?`. Lines are clipped with an
  ellipsis rather than wrapping. A 20×4 terminal fits the full four-line layout
  (long identities may be clipped). Shorter windows use a compact `POC/RO`
  layout; at one/two rows the quit hint takes priority. Enlarging restores text.
- Runtime: scratch, UID/GID 1000, passwd/group records, no shell or credentials.
  Bubble Tea **v2.1.0** and all module dependencies are pinned in `go.mod`/`go.sum`;
  the Go **1.26.7** builder is digest-pinned in the Dockerfile.
- Agent: official **`containerssh/agent` v0.10.0** copied to
  **`/usr/bin/containerssh-agent`** from
  `sha256:a4cc5356ccd25df99b5ccf73a567dcb024601d3448b989154f9cf152cf4fbdbb`.
  That published image is **amd64-only**; the build rejects other runtime targets.
  The upstream pod command replaces the image entrypoint with
  `/usr/bin/containerssh-agent wait-signal --signal INT --signal TERM`.
  Agent `console --pid --env NAME=VALUE -- /usr/local/bin/dev-box-settings`
  supports the connection-mode session execution path.

The image does **not** enforce SSH request policy or establish that environment
values are trusted. Gateway policy must reject client environment, exec,
subsystems, forwarding, and extra session channels. `q` exits the application
session, not an idle agent or multiplexed SSH connection. Connection-mode pod
cleanup occurs on full SSH disconnect. Docker checks do not validate that cleanup,
Kubernetes isolation, or end-to-end authentication/authorization; live validation
and rollout require separate approval.
