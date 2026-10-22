# Tests

## Offline regression checks

Requires Go (the modules select their toolchains), Helm, Ruby with YAML, and Python 3.
Install the pinned chart dependency once, then run:

```sh
helm dependency build charts/containerssh
./tests/check.sh
```

The check builds, vets and race-tests the three Go applications and the config-merge
validator, runs the chart rendering suites, and syntax-checks Python probes. It never
connects to Kubernetes or starts Docker. Initial dependency/toolchain downloads need
network access. The Settings executable tests require a Unix PTY.

| Check | Protects |
| --- | --- |
| Application `*_test.go` | Auth/config contracts, owner access, storage, TUI model and real-process behavior |
| `chart-base-rendering.sh` | Unconditional shared-base restrictions, empty bases and ordinary-template overrides |
| `chart-networkpolicy-rendering.sh` | Session isolation and scoped Settings API egress defaults, switches and overrides |
| Other `chart-*-rendering.sh`, `chart-service-selector.sh`, `chart-template-authorization.sh` | Helm wiring, Settings, auth, persistent mode, RBAC, Kata, storage and Service isolation |
| `config-merge/` | Actual ContainerSSH defaulting, merge and security/backend validation; also used by the Docker check below |

## Docker image checks (no cluster)

Requires a running Docker daemon; images are linux/amd64 (ARM hosts need emulation).
Run these separately from the fast offline checks:

```sh
# Use the intended immutable gateway image, not a moving deployment tag.
export CONTAINERSSH_IMAGE='<reviewed-gateway-image@sha256:digest>'
bash tests/chart-config-binary-validation.sh
bash tests/gateway-error-messages.sh
python3 tests/auth-server-error-messages.py

docker build --platform linux/amd64 -t dev-box-settings:local settings-tui
python3 tests/settings-image-smoke.py --image dev-box-settings:local
```

- Binary validation tests rendered bases and synthesized webhook merges through the
  real merger/validators and gateway `--dump-config`.
- Gateway message tests use a stub webhook; auth-server message tests build the real
  auth image and use a fake authentik. These cover different failure boundaries.
- Settings smoke tests drive the packaged TUI over a PTY against a stand-in API,
  including delete/recreate simulation. They do not enforce SSH request policy.

## Live acceptance (explicit approval required)

Read [operations](../docs/operations.md) before using an enrolled key or cluster
credentials. These are deliberately excluded from `check.sh`: opening an SSH
connection can create pods/PVCs even when the probe itself only observes Kubernetes.
Use each script's `--help` for required arguments and bounds.

| Probe | Purpose / side effects |
| --- | --- |
| `ssh-template-rejection.py` | Known-template positive control, then prompt unknown-template denial |
| `ssh-settings-policy.py` | Real SSH request denials, terminal behavior, optional two-owner and pod-lifetime observations |
| `ssh-settings-lifecycle.py` | Kills its own SSH client and exercises a control master; checks connection-pod cleanup and ordinary pod UID preservation |
| `settings-owner-authorization.py` | Read-only operator impersonation checks; does not prove mounted-token use or network reachability |

The policy probe requires `paramiko==3.5.1`; install it in a disposable virtual
environment. Use trusted `known_hosts`, explicit context/namespace and verified pod
names/selectors. Read probe options before running; ordinary positive controls use
`ubuntu`. UI deletion and storage-survival acceptance must use an approved disposable
box, not the owner's ordinary box.

Historical rollout reports, draft plans, the stock-gateway patch build and the
special fault-injection fixture have been retired. Git history retains their records;
this directory contains maintained checks, not a deployment journal. The retired
fixture's crash/missing-owner injection is no longer a supported test capability.
