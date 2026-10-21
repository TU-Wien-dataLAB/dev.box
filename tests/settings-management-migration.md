# How to accept the settings management UI (issue #7)

> **Status: rollout completed 2026-10-21** — revisions 42–44, all gates passed; see
> [the live evidence](settings-management-2026-10-21.md). This document remains the procedure.

Scope: [dev.box issue #7](https://github.com/TU-Wien-dataLAB/dev.box/issues/7) — the settings TUI
grown from the read-only #9/#11 POC into the live management interface (box rows, delete with
confirmation, honest connection status). Issues #12 (PVC) and #13 (metrics/top) remain open
follow-ups; #10 (two real owners) gates pre-alpha enrollment, not this rollout.

**Approval boundary:** this is a procedure, not rollout approval or evidence. Complete all local
gates first. Obtain separate explicit approval before any Helm upgrade, port-forward, SSH
connection, cluster observation, authorization probe or cleanup. Mark every gate PASS / FAIL /
BLOCKED / NOT RUN with its actual seam. Do not treat this document, issue comments, or green local
tests as approval.

## What changed (and what did not)

- `settings-tui`: one row per trusted template (template, status, uptime, image, runtime class,
  restarts, limits), ~3 s polling plus `r`, `j`/`k`/arrows selection, `d`+`y` delete with the
  three-consequence confirmation, Terminating → not-created transition, Live/Partial/Unreachable
  status, 403 reconnect hints, `NO_COLOR`/`TERM=dumb` plain rendering, tiny-terminal collapse.
  Kubernetes access is per-name pod get/delete behind a small client interface
  (`DEV_BOX_KUBE_API_URL` is the documented offline-validation override for the Docker smoke; the
  config server never sets it). Quit/crash never exposes a shell; the image remains shell-free.
- `config-server`: `DEV_BOX_BOX_TEMPLATES` env beside the verified owner, from the same template
  walk that computes the owner Role's `resourceNames`; unreadable templates still deny settings.
- Chart: one new NetworkPolicy `containerssh-sessions-settings-api`
  (`networkPolicy.settingsApiEgress`): settings pods (`dev.box/application=settings`) may egress
  to the Kubernetes API only. Ordinary pod policy, RBAC, naming, cap and ordinary box lifecycle
  are unchanged. The security floor of the settings connection (connection mode, forced
  application executable, one session, exec/subsystem/env/signal/forwarding denied) is unchanged.
- Unchanged: authentication, auth-server allowlist wiring, persistent naming/cap/reconnect,
  per-owner RBAC (get/delete on exact deterministic names), host keys, tokens.

## 1. Local gates (run from the repository root)

```bash
(cd config-server && go build ./... && go vet ./... && go test -race ./...)
(cd auth-server && go build ./... && go vet ./... && go test -race ./...)
(cd settings-tui && go build ./... && go vet ./... && go test -race ./...)
helm dependency build charts/containerssh
tests/chart-auth-rendering.sh
tests/chart-template-authorization.sh
tests/chart-service-selector.sh
tests/chart-kata-rendering.sh
tests/chart-persistent-rendering.sh
tests/chart-settings-rendering.sh
CONTAINERSSH_IMAGE='<reviewed gateway reference>' tests/chart-config-binary-validation.sh
docker build --platform linux/amd64 -t dev-box-settings:issue7 settings-tui
docker build --platform linux/amd64 -t config-server:issue7 config-server
python3 tests/settings-image-smoke.py --image dev-box-settings:issue7
```

Required local coverage before rollout (all exercised by the suites above — record revisions,
image IDs/digests, and outcomes):

1. Settings-tui Go tests: golden deterministic naming against the live cluster's known box name;
   row assembly from gets (Running/Pending/Terminating/Error/not-created), 403/transport failure
   handling (last-known dimming, reconnect hint, Partial/Unreachable), delete confirmation and
   cancel semantics, Terminating transition, empty template list, NO_COLOR, tiny terminals — plus
   the full executable PTY matrix against a stand-in Kubernetes API (list render, selection via
   the dialog, confirm/cancel, manual refresh, token enforcement, dumb terminal, malformed template
   list fail-closed, no-PTY and missing-owner failures).
2. Docker image smoke: the management flow over the real image (rows, selection,
   confirm/cancel, refresh, Terminating, not-created, recreate hint), degraded terminals,
   unreachable degradation, shell-free image, agent console/idle-signal compatibility.
3. Config-server tests: `DEV_BOX_BOX_TEMPLATES` equals the Role's template walk (sorted, empty
   list valid, unreadable templates deny), env is exactly owner+templates, ordinary behavior
   unchanged.
4. Chart rendering: the settings API egress NetworkPolicy (scoped selector, one rule, 443/6443
   only, base policy unchanged, both disable switches, operator overrides) and unchanged
   ordinary defaults.
5. Real-binary validation: the merged settings config (including both env vars) through the real
   merger and the release gateway binary.

## 2. Rollout (after approval)

1. Record approval, operator, release revision, time and cleanup bounds. Publish immutable images:
   the new settings-tui image and the rebuilt config-server image, both pinned by digest. The
   chart's `settings.image` default is not the live pin — set the reviewed digests in user values
   and keep the existing gateway/auth-server pins. New settings images apply to new connections
   only; old settings pods keep running until their connection ends.
2. Verify the rendered `containerssh-sessions-settings-api` NetworkPolicy against the actual
   cluster: confirm where the API server runs (kubeadm-style control planes match the default
   kube-system `kube-apiserver` selector; a k3d-style server needs an `ipBlock` override) and that
   ordinary pods' egress is unchanged. Calico enforces post-NAT, so both 443 and the pod port 6443
   are allowed.
3. Upgrade the release (expect the gateway deployment to roll; active SSH sessions may be
   interrupted). Preserve host-key/authentik Secret references and all ordinary template values.
4. Sanity checks, each explicitly approved before connecting:
   - `ssh -t settings@…` as the existing single enrolled owner: rows render for every published
     template, Connection Live, refresh ticks, not-created rows show the create command.
   - SSH policy probes re-run unchanged: `tests/ssh-settings-policy.py` (exec, subsystem, env,
     signal, forwarding still denied; one session per connection) and
     `tests/ssh-settings-lifecycle.py` patterns (abrupt disconnect, OpenSSH master lifetime).
   - Ordinary persistent regression: reconnect to the existing `ubuntu` box reuses the same pod
     name and UID; the box cap and reconnect exemption are unchanged.
5. **Real delete and recreate of an operator-owned test box** (the core loop): from the settings
   UI, delete the test box, watch the row go Terminating then not-created, reconnect
   `ssh <template>@…` and confirm a fresh box with the same deterministic name is created.
   Record the pod UIDs before/after (they must differ) and confirm the owner's other boxes are
   untouched.
6. Owner-authorization helper: re-run `tests/settings-owner-authorization.py` (impersonated
   get/delete on exact names succeeds, list/watch/exec and foreign names are denied) and confirm
   no new grants exist beyond get/delete on deterministic names.
7. Cleanup: explicitly delete test boxes and any test templates with UID preconditions; leave the
   owner's real boxes running.

## 3. Evidence and limitations

Record results in a new `tests/settings-management-<date>.md` following the existing evidence
format: gates with PASS/FAIL/BLOCKED, exact commands, revisions, image digests, timings, and
limitations. Known limitations to state explicitly:

- The `ssh <template>@<host>` hint in not-created rows shows a `<host>` placeholder; the TUI has
  no trusted knowledge of the public SSH hostname.
- API egress has been validated by rendering and (after rollout) by a real settings-pod request;
  impersonation proves authorization, not network reachability.
- Two-real-owner identity testing (#10) remains open and gates pre-alpha enrollment, not this
  rollout.
- Delete remains destructive (no PVC — #12) and resource usage is not shown (#13).
