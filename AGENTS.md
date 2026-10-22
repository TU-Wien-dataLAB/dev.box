# dev.box — agent instructions

ContainerSSH v0.6 + Kubernetes: `charts/containerssh/` deploys the gateway;
`auth-server/` resolves SSH keys through authentik; `config-server/` selects templates
and synthesizes Settings; `settings-tui/` manages the owner's boxes.
Ordinary boxes are persistent pods, not automatically durable filesystems.

## Read before working

- **Deployment, storage activation/cleanup, Settings/RBAC/network changes, or live acceptance:**
  read [docs/operations.md](docs/operations.md) first, including pending ubuntu activation.
- **Template/base changes:** read [chart contracts](charts/containerssh/README.md),
  especially retained storage and reserved Settings; consult `values.yaml` for defaults.
- **Webhook or UI changes:** read the corresponding component README and tests.
- **Validation:** [tests/README.md](tests/README.md) is the command reference;
  `./tests/check.sh` is the offline runner. Docker and manual live checks are separate.
  Historical reports and retired fixture results are not current acceptance.

## ContainerSSH fork workflow

- Gateway source fixes belong to `TU-Wien-dataLAB/ContainerSSH`, not this repository
  or upstream `ContainerSSH/ContainerSSH`. `origin` is our fork; `upstream` is upstream.
- Reference checkout `/Users/matthiasmatt/Documents/Work/oss/ContainerSSH` is inspection-only.
  Before editing gateway source, ask: “Please create a new ContainerSSH fork worktree
  in herdr and share its absolute path and branch name.” Wait for the manually created
  worktree; use an already-confirmed worktree when supplied. Never create it automatically.
- Commit/push from that worktree. Confirm the PR base branch with the user, then use
  `gh pr create --repo TU-Wien-dataLAB/ContainerSSH --base <confirmed-base>`.
- Keep chart/webhook/UI changes and deployment evidence in `dev.box`. Source PR approval
  does not approve deployment. Retired local gateway patch tooling is not maintained.

## Live safety

- Default to offline work. Obtain explicit approval before **any** real cluster observation,
  export, port-forward, SSH probe, authorization probe, rollout, fixture or cleanup.
- Every Helm command uses `--kube-context container-ssh --namespace containerssh`;
  every kubectl command uses `--context container-ssh` and an explicit namespace
  (`containerssh` management, `containerssh-sessions` backends). Never trust defaults.
- Preserve full reviewed user values, immutable image pins, ordinary pod UIDs, host identity
  and Secret references. Helm replaces lists; do not replace templates with minimal examples.
- Recreation/data-loss and retained-PVC deletion require specific approval. No broad cleanup,
  namespace purge or uninstall as a testing shortcut. Use exact UID-preconditioned cleanup.
- Keep credentials/exports outside git with restricted permissions; never print tokens.
  Authentik is read-only; use existing enrolled keys and independently verified host trust.

## Contracts that must survive changes

- SSH username selects `<name>.yaml`, then `<name>.json`, else denial; `default` is ordinary,
  not fallback. Empty/unsafe names deny. `settings` is code-owned, never a template item.
- Auth performs one exact `attributes.sshPublicKey` single-element-list query, without
  normalization/scanning. Exactly one owner authenticates; zero/multiple deny. Login template
  and verified owner differ intentionally. Bundled password auth always denies.
- Render `auth.publicKey`, **not** `auth.pubkey` (legacy boolean). Authz alone is not auth.
  Bundled allowlist is ordinary names + settings; external authz needs its own early gate.
  Both webhooks fail closed; config failures retry every 10 s, not prompt policy denials.
- Persistent name = `box-` + first 10 hex of SHA-256(trimmed verified owner + NUL + template),
  with `dev.box/owner`. Inject `createMissingPods: true` only in persistent responses,
  never the shared base. Cap defaults to 3; reconnect passes, list errors/blank identity deny.
- Real merge is `mergo.WithOverride`: empty/nil/false sources cannot clear inherited fields.
  Nonempty container slices **replace** the base: restate image, security and limits.
  Validate effective configs through the real merge/backend/security validators;
  `--dump-config` parsing alone does not prove policy.
- Shared base spec allows only containers, securityContext, restartPolicy and
  terminationGracePeriodSeconds. Base securityContext allows runAsNonRoot/runAsUser/
  runAsGroup/seccompProfile.type; metadata only labels/annotations excluding dev.box/owner.
  Heavy fields belong in ordinary templates. Chart owns namespace/generateName; avoid duplicates.
  Kata is injected per ordinary template, never into the base or Settings.
- Settings: connection mode, direct `/usr/local/bin/dev-box-settings`, PTY/application shell,
  one session per transport; exec/subsystem/env/signal/forwarding denied. Trusted owner env
  remains raw verified identity; display sanitization never changes authorization.
- Owner SA/Role/Binding = `dev-box-owner-` + first 48 hex of SHA-256(trimmed verified owner).
  Grant pod get/delete only on exact ordinary persistent names; zero names means **zero rules**,
  never an empty resourceNames grant. Template env and Role derive from the same walk.
  Account automount false; Settings pod true with scoped projected token. No automatic object GC.
- Config-server holds namespace pod list/get/delete and SA/Role/Binding get/list/create/update;
  delegated pod rights are necessary for anti-escalation, not bind/escalate. Managed storage
  adds PVC get/create only. Reconcile/API/template errors deny; partial/old grants can remain.
- Ordinary pods: no ingress, internet-only egress + DNS. Only Settings gets scoped API egress
  (443/6443; verify cluster topology/post-NAT). Impersonation is not token/network proof.
- Persistent disconnect retains the pod; deletion/eviction/recreation loses writable-layer data.
  Retained PVCs are per-template opt-in, not backups. Template changes mutate neither existing
  pods nor claims. Retained claims/owner objects require deliberate operator lifecycle control.
- Backend images need compatible `/usr/bin/containerssh-agent`; plain Ubuntu is not a drop-in.
  Gateway overrides need Settings fixes **and auth message support**; stock v0.6 rejects messages.
- Bundled webhook Services use `:8080`; SSH Service must select component `server`.
  Config changes must roll the gateway; external ConfigMaps need an approved explicit restart.
  Config-server startup requires Settings image + in-cluster client in every mode; use handler
  fixtures offline, not host `go run` as a substitute for Kubernetes.
- Log levels are syslog-numbered: 7 debug, 6 info, 5 notice, 4 warning, 3 error, 2 crit.
