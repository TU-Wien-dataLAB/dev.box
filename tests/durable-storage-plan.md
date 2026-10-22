# Issue #12: durable box storage plan

Source: [issue #12, settled design](https://github.com/TU-Wien-dataLAB/dev.box/issues/12).
This plan is not release rollout approval. The user approved isolated tests in context
`container-ssh`; existing release values and ordinary boxes must remain unchanged.

## Sequence and agreed test seams

1. Before implementation, create an isolated operator PVC and Kata pod using the existing
   guest image. Run as UID 1000 with fsGroup 1000; write a marker in the PVC and in /tmp,
   delete/recreate only the test pod, verify retained marker and lost ephemeral marker.
   Delete only the recorded test resources afterward. Record runtime, CSI facts and timings.
2. Config webhook → Kubernetes API boundary: native claim-template envelope, explicit positive
   storage size, persistent effective mode, verified owner/template/claim identity, create/get
   only, cap-before-creation, copy-before-rewrite, concurrent owner isolation, partial-failure retry.
3. Helm rendering boundary: opt-in declarations, persistent effective-mode validation, explicit
   sizes, create/get PVC permissions, conditional namespace ResourceQuota (500Gi / 50 claims).
4. Settings rendering boundary: every confirmation layout says:
   "Local disk data is destroyed; data on retained PVCs is kept."
5. Run focused tests during development, then all Go suites, chart suites and real-binary merge
   validation where available. Review Standards and Spec independently; commit on current branch.

## Contract

Managed claims are per (trimmed authenticated owner, template, claim), named
`<deterministic-box-name>-<claim>`. Symbolic PodSpec PVC references matching a claim template
are rewritten; unmatched references remain operator-managed. Claims have query labels and exact
identity annotations, no pod owner references. Reuse verifies identity and rejects termination;
existing specifications are not reconciled. Storage is excluded from settings and nonpersistent
modes. Cached template specs must not be mutated. Failure denies the connection, without rollback
of already-created retained claims. The existing live-pod cap is enforced before claims are created.

Mounts, fsGroup, HOME, access modes, storage class and explicit sizes belong to operator templates.
Pod/container merge semantics remain unchanged. **Correction to the issue's recorded fact:**
the real v0.6 merger replaces a nonempty containers slice, not element-wise merging. Storage
examples therefore restate image, container security and resource limits; no new merger is added. Retention does not imply backups or protection from
PVC/namespace deletion, storage failure or users deleting files. Template changes affect neither
existing pods nor claims. Renamed/removed claims accumulate until explicit operator cleanup by label;
quota is namespace-wide, not per-owner fairness. Expansion is a manual operator patch.

## Later staged rollout (requires separate approval)

Publish immutable config-server/settings images; render and review full preserved live values.
Do not delete the existing ordinary box. Use an isolated ordinary test template to exercise real
SSH creation latency, managed claim identity/isolation, quota counting, and settings delete →
reconnect with retained data and lost ephemeral data. Re-run SSH policy/lifecycle, prompt unknown
name denial and ordinary UID-preserving reconnect. Record PASS/FAIL/BLOCKED/NOT RUN separately;
the operator-created compatibility spike alone does not prove managed provisioning or gateway/TUI
integration. No automatic GC, resizing controller, storage UI or ContainerSSH fork changes.
