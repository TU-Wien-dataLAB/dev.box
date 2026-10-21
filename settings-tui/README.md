# Settings TUI

Standalone Go + Bubble Tea application: the dev.box management UI. It is not an SSH server (no
Wish, no shell — there is no shell in the image). Over `ssh -t settings@host` it shows a
master–detail screen — one master row per box template published for the verified owner
(name left, state right), and a detail pane for the selected box with its status strip
(status, uptime, restarts) and ledger (image, runtime class, resource requests and limits) —
refreshed every few seconds from the Kubernetes API, plus a per-owner box-cap line. It offers
exactly one management action: **delete** (confirm with `y`).

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

The Go tests launch a separately built executable through real PTYs, some against an in-process
stand-in Kubernetes API server, so list rendering, delete confirmation, refresh, and the
Terminating-to-not-created transition are all exercised offline. The Python smoke check drives the
real Docker image the same way (stand-in API in a sidecar container) and additionally checks
resize, quit/terminal restoration, inert command/suspend/escape input, identity sanitization,
startup failures, shell absence, and agent console/idle-signal compatibility.

## Execution contract

- Executable: **`/usr/local/bin/dev-box-settings`**, launched directly without a shell wrapper.
  The Docker entrypoint is this executable.
- Identity: **`DEV_BOX_AUTHENTICATED_OWNER`** must contain the nonempty canonical owner supplied
  by trusted gateway/config-server plumbing. Unset, empty, and whitespace-only values fail closed
  with exit 1. No username, image-user, or original-command fallback exists. Local examples use a
  synthetic identity.
- Box templates: **`DEV_BOX_BOX_TEMPLATES`** carries the owner's ordinary persistent template
  names, comma-separated, from the same trusted plumbing. Unset/empty is a valid empty list; any
  malformed entry fails closed with exit 1. The TUI derives each box's deterministic name itself
  (`box-` + first 10 hex of SHA-256 over trimmed owner + NUL + template — the same contract as the
  config server's persistent naming, pinned by golden tests against the live cluster). Template
  names never come from client input.
- Box cap: **`DEV_BOX_MAX_BOXES`** carries the operator-set per-owner box cap from the same trusted
  plumbing (the config server injects its enforced `CONTAINERSSH_MAX_PODS_PER_USER`; `0`/unset
  hides the element). The master pane foots it as `created N/max` with a block bar; at the cap
  the bar turns yellow and names the way out. The count is advisory display data derived from the
  owner's own rows (any phase counts, including Terminating; not-created rows do not): the TUI has
  no pod-list permission, and the config server's list-based check stays authoritative — a box
  created from a template no longer published to this owner is invisible to the count but still
  consumes the real cap. Malformed values fail closed with exit 1.
- Kubernetes access: per-name pod **get** and **delete** through the pod's mounted projected
  token, using in-cluster configuration (service env, token file, CA) in the pod's own namespace.
  No list, watch, or other verb exists in the client. The whole surface sits behind a small
  `boxClient` interface so all management logic is testable offline against a fake.
  **`DEV_BOX_KUBE_API_URL`** (with optional `DEV_BOX_KUBE_API_TOKEN` and required
  `DEV_BOX_NAMESPACE`) is an offline-validation override pointing the same code at a stand-in API
  server for the Docker smoke; it is trusted plumbing exactly like the owner env and is never set
  by the config server. A missing/unreachable API never crashes the app: it degrades to an honest
  **Unreachable** connection state, keeps last-known rows dimmed, and disables delete.
- Terminal: stdin and stdout must both be terminals with nonzero initial output dimensions.
  Initial dimensions may arrive after the agent PID handshake; startup waits up to two seconds for
  a zero-sized PTY to become usable. Otherwise it exits 1 with a helpful `ssh -t` hint, without
  opening a fallback `/dev/tty`. Non-PTY use fails immediately; checks precede Bubble Tea execution.
- UI: `↑/↓` or `j`/`k` select (the detail pane follows); `r` refreshes now (plus an automatic poll
  every ~3 s); `d` opens a centered delete-confirmation popup for the selected existing box; `y`
  confirms; `n`/`Esc`/`q` cancel. `q` or Ctrl+C quits from the list view; inside the popup they
  only cancel. The popup states all three consequences: local data destroyed (no PVC exists),
  active sessions killed, and a fresh box on the next login to that template. A deleted box shows
  **Terminating** until it is gone, then settles into the same dimmed **not created** row as
  never-instantiated templates; the detail pane of a not-created box names the exact
  `ssh <template>@<host>` command that creates it.
- Honesty: a status line reports **Live**, **Partial**, or **Unreachable**; failed rows keep
  their last-known state dimmed; a per-name authorization failure (403) shows a reconnect hint
  because the template list and grants are fixed per settings connection; delete is unavailable
  unless the connection is Live, so stale information is never acted on. Zero templates render an
  explicit empty state with delete unavailable.
- Display: all dynamic text (owner, images, runtime classes, API messages) is sanitized to printable
  runes — controls, bidi/format characters, and non-printing separators become `?`; nothing is ever
  parsed as ANSI markup. The master list shows only the template name and the state word
  (status-tinted: running green, pending/terminating yellow, error red, not-created dim; the footer
  right-aligns the verified owner and connection state). Lines are clipped with an ellipsis rather
  than wrapping; terminals without room for the two panes collapse to a master-only view, then to
  single-column rows, then compact headers, and at one/two rows the quit hint takes priority.
  `NO_COLOR` or `TERM=dumb`/unset fall back to plain text.
- Runtime: scratch, UID/GID 1000, passwd/group records, no shell or credentials. Bubble Tea
  **v2.1.0** and all module dependencies are pinned in `go.mod`/`go.sum`; the Go **1.26.7** builder
  is digest-pinned in the Dockerfile. The Kubernetes client is hand-rolled over the standard
  library (`net/http`), so the image stays dependency-free and small.
- Agent: official **`containerssh/agent` v0.10.0** copied to **`/usr/bin/containerssh-agent`**
  from `sha256:a4cc5356ccd25df99b5ccf73a567dcb024601d3448b989154f9cf152cf4fbdbb`.
  That published image is **amd64-only**; the build rejects other runtime targets.
  The upstream pod command replaces the image entrypoint with
  `/usr/bin/containerssh-agent wait-signal --signal INT --signal TERM`.
  Agent `console --pid --env NAME=VALUE -- /usr/local/bin/dev-box-settings`
  supports the connection-mode session execution path.

The image does **not** enforce SSH request policy or establish that environment
variables arrive only from trusted plumbing; the ContainerSSH security config owns that
(the chart's `configserver` values and the config server's synthesis). Live acceptance of the
management actions is approval-gated; see
[the management acceptance plan](../tests/settings-management-migration.md).
