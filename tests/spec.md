# Spec — ContainerSSH webhook contract test framework

Status: proposed (local draft — written via the `to-spec` skill; not yet published to an issue
tracker or triaged).

## Problem Statement

The repo ships two webhook servers that ContainerSSH relies on at connection time — the
**config-server** (username-selected pod templates, fail-closed) and the **auth-server**
(authentik-backed SSH public-key lookup, fail-closed). Today:

- both are only ever verified **in-process** (`go test`, auth-server against an in-memory
  authentik mock), and
- the existing CI only proves the **Docker images build** (`.github/workflows/*-image.yml`).

Nothing confirms that the **built images actually speak the ContainerSSH webhook contract over the
wire**. A regression can ship silently and only surface as "SSH is broken" after a real deployment:
a packaging error, a dropped env var, a wire-shape drift, ghcr image that boots but answers
garbage, an auth-server that exits at startup because authentik is unreachable from the pod. As a
maintainer I have no cheap, scriptable, local check that says "these two images still satisfy the
webhook contract."

## Solution

A **contract test framework** that, from a single command:

1. builds both server images from their existing Dockerfiles (no pre-push to a registry needed);
2. runs them on a private docker network alongside a throwaway **fake authentik** (the auth-server
   refuses to boot without a reachable `AUTHENTIK_URL`, so a stand-in is mandatory);
3. fires one **canonical webhook request per server** through ContainerSSH's **own reference client
   libraries**, asserts on the responses, and
4. exits non-zero on any mismatch so both humans and CI see the result.

The framework is thin orchestration **+ a small Go "contract runner"**. Crucially, the runner
asserts through the same client code ContainerSSH ships (`config/webhook.NewTestClient` and
`auth/webhook.NewTestClient`), so "does the image speak the contract?" is answered by the actual
consumer — not by hand-rolled JSON expectations. A docker-compose file wires builds (from the
Dockerfiles), the network, and fixtures; the runner's exit code gates the whole run via
`--exit-code-from`.

## User Stories

1. As a maintainer, I want to confirm that the built `config-server` image answers a canonical
   config webhook request with the pod template selected by SSH username, so that the username →
   template contract is proven over the wire, not just in-process.
2. As a maintainer, I want to confirm the template fallback chain (`<username>` → `default` → empty
   base config) against a built image, so that unknown-user behavior is verifiable.
3. As a maintainer, I want to confirm that a built `config-server` returns a `config`-keyed JSON
   envelope that the reference client can decode, so that response-shape regressions are caught.
4. As a maintainer, I want a built `auth-server` to return success with the correct owner when an
   enrolled fingerprint is presented, so that the authentik-backed pubkey path is verified against
   the real image.
5. As a maintainer, I want a built `auth-server` to **cleanly deny** (no 500) an unknown or
   un-enrolled fingerprint, so that deny semantics are confirmed rather than assumed.
6. As a maintainer, I want a built `auth-server` to deny password auth for a username not on the
   test allowlist, so that the key-first stance is confirmed at the image level.
7. As a maintainer, I want a built `auth-server`'s `/config` endpoint to return an empty config, so
   that its base-config-passthrough role is confirmed.
8. As a maintainer, I want the framework to assert the auth-server's HTTP hygiene (405 on non-POST,
   404 on unknown paths), so that routing regressions are visible.
9. As a maintainer, I want the auth-server to run against a **fake authentik** so that it boots and
   serves /pubkey without a real instance, and so that OnPubKey's deny-vs-allow decision (driven by
   the fake's pagination count) can be controlled deterministically.
10. As a maintainer, I want the whole check to run with **one command** locally, so that it's part
    of my normal pre-push loop.
11. As a maintainer, I want the check to build every image from its Dockerfile, so that the exact
    artifacts that would be deployed are the ones being tested.
12. As a maintainer, I want the check to run on a private docker network with no host port
    mappings, so that it is side-effect-free and parallel-safe on a dev machine.
13. As a maintainer, I want the runner to wait for server readiness before asserting, so that the
    check is robust to container startup timing.
14. As a maintainer, I want the runner to print a clear OK/FAIL table and exit non-zero on failure,
    so that I can see at a glance which assertion broke and CI can gate on it.
15. As a maintainer, I want the same check to be runnable in CI, so that merged code is gated on
    "images build **and** speak the contract."
16. As a maintainer, I want fixtures (pod templates, seeded authentik users, a fixed SSH test key)
    to be checked into the repo, so that the check is deterministic and reproducible on any machine.
17. As a maintainer, I want the runner to assert through ContainerSSH's own reference client
    libraries, so that the test confirms compatibility with the actual production consumer rather
    than my own guess of the JSON.
18. As a maintainer, I want the framework duplicated or layered in such a way that the existing fast
    unit tests (`go test ./...`) remain the first line of defense, so that I keep fast feedback for
    handler logic alongside the slower image-level check.
19. As a maintainer, I want to be able to add a pod template fixture and see an assertion for it
    without re-architecting the harness, so that the matrix grows cheaply.
20. As a maintainer, I want the fake-authentik stand-in documented with the single authentik API
    endpoint it implements, so that future maintainers understand it is a stub and not a real
    integration.
21. As a maintainer, I want a documented decision of what is deliberately **not** tested by this
    framework (real SSH handshakes, fail-closed kill tests, TLS/mTLS, the sync), so that its scope
    and gaps are explicit.

## Implementation Decisions

### Seam (single)

The framework tests at **one seam: the HTTP wire between the built server images and the reference
ContainerSSH client libraries.** This is the highest seam available that verifies the artifacts
that actually ship (the Docker images), without requiring a full SSH handshake. The images are the
unit under test; the runner is the reference consumer; the fake authentik is a test double at the
servers' upstream boundary.

- **Config contract** is asserted via `go.containerssh.io/containerssh/config/webhook.NewTestClient`
  → `Get(ctx, meta)`, which round-trips through the exact production client code
  (`internal/config/client_impl.go`: POST to the base URL, retry/deny-on-non-200 semantics,
  `RemoteAddress` string marshalling).
- **Auth contract** is asserted via `go.containerssh.io/containerssh/auth/webhook.NewTestClient` →
  `PubKey(meta, key)` / `Password(meta, pw)`, round-tripping through the exact production client
  (`internal/auth/webhook_client_impl.go`: `/pubkey` & `/password` paths, `passwordBase64`
  encoding, `success`-driven decision).
- The `NewTestClient` variants exist precisely because the plain public clients require internal
  metrics collectors; the test factories build the production HTTP client internally. Both are
  pinned to `go.containerssh.io/containerssh` v0.6.0 (same as the two server modules).

### Modules built

New test-only modules under the repo's tests area (this spec is intentionally path-free, but the
shape is): a **contract runner** (small Go module with its own Dockerfile mirroring the servers'
two-stage build), a **fake authentik** (tiny stdlib-only Go server with its own Dockerfile), a
**docker-compose file**, and **fixtures** (pod-template YAMLs, seeded authentik users JSON, one
fixed SSH test key). No changes to `config-server/` or `auth-server/` source are anticipated unless
the check surfaces an actual bug.

### docker-compose — thin orchestration only

Compose exists for four mechanical jobs and nothing else: (1) build each image from its own
Dockerfile via `build.context`; (2) place the containers on one private network with fixed service
names (no published ports — the runner reaches peers by service name on the network); (3) mount
fixtures as volumes; (4) forward the runner's exit code with
`up --build --abort-on-container-exit --exit-code-from <runner>`. No health-gate logic in compose —
the runner performs its own wait-for-readiness. If compose is later undesirable, a plain
`docker run --network` script can replace it without changing the runner or the fixtures.

### Contract runner behavior

- Idempotently polls the config/auth endpoints until the servers respond, then runs the assertion
  matrix.
- Each assertion is a named check producing a `CONTRACT OK/FAIL <name>` line; the runner prints a
  table and exits non-zero on the first failure or at the end if any failed.
- Ready-state observation comes from trying real requests (optionally after a fixed sleep), not from
  log scraping.
- The runner is the only container that exits; servers are torn down by `compose down`.

### Assertion matrix (API contract — grounded in the v0.6 source)

| Route | Request | Expected |
| --- | --- | --- |
| config-server `POST /` | metadata + `username:"ubuntu"` | 200; reference `Get` returns the `ubuntu.yaml` template (`kubernetes.pod.spec.containers[0].image == "ubuntu:22.04"`) |
| config-server `POST /` | `username:"somebody-else"` | 200; falls back to `default.yaml` template |
| config-server `POST /` | no matching template / no default | 200; empty config |
| config-server raw | minimal `POST /` body | 200, `Content-Type: application/json`, `config` key present |
| auth-server `POST /pubkey` | enrolled fingerprint, `username:"alice"` | `Success()==true`, authenticated as owner `alice` |
| auth-server `POST /pubkey` | unknown fingerprint | `Success()==false`, no error (clean deny, not 500) |
| auth-server `POST /password` | user not on `AUTH_SERVER_PASSWORD_USERS` | `Success()==false` (default deny) |
| auth-server `POST /config` | any | 200, `{"config":{}}` |
| auth-server raw | `GET /pubkey` / unknown path | 405 / 404 |

### Fake authentik (test double) — endpoint contract

Implements **a single authentik REST endpoint: `GET /api/v3/core/users/`** (the UsersList API),
plus optionally `PATCH /api/v3/core/users/{id}/` for a future sync stage. It does not implement
flows, login, `/me`, token validation, or schemas.

- Request the fake must honor: `Authorization: Bearer <token>` (checked best-effort — log and
  continue, since the auth-server never verifies the token against authentik), `Accept:
  application/json`, and any of the client's query params:
  - `attributes={"ssh_key_fingerprint":"SHA256:…"}` (URL-encoded JSON string) — the pubkey hot path,
  - `username=<name>&groups_by_name=<group>` — the authz group gate (optional),
  - `page_size=100&ordering=username` (+ `page=N`) — sync walk (only if sync is exercised).
- Response envelope (decoded by `authentik.go`'s `paginatedUsers`): `pagination` (object with
  `count`, `current`, `total_pages`) plus `results` (array of user objects exposing at least `pk`,
  `username`, `is_active`, `attributes.ssh_key_fingerprint`, `groups_obj[].name`).
- **Semantics that drive the server's decision:** the fake must genuinely filter its seeded users
  against the decoded filter and report the true count, because `OnPubKey` branches on
  `pagination.count`: `0` → deny, `1` → authenticate as `results[0].username` (after checking
  `is_active` and, if enabled, username binding), `>1` → integrity error → HTTP 500.
- Implementation is stdlib-only (no `x/crypto`): the seeded enrolled key is **one fixed test key
  whose SHA256 fingerprint is a hardcoded constant** (derive once via `ssh-keygen -lf`), kept in
  sync between the seed data and the runner. The handler logic is lifted from the existing
  `mockAuthentik` in the auth-server's test suite.
- Optionally seeds a second, `is_active:false` user to cover the inactive-account deny path.

### Wire-contract facts the framework encodes (verified against ContainerSSH v0.6 source)

- Config request = `ConnectionAuthenticatedMetadata` inlined (`remoteAddress` as an `"ip:port"`
  string, `connectionId`, `clientVersion`, `username`, `authenticatedUsername`, and the
  `metadata`/`environment`/`files` maps); config response = `{"config": <partial AppConfig>, …}`;
  non-200 → ContainerSSH denies and retries every 10 s.
- Auth request adds `publicKey` (authorized-keys format) on `/pubkey` and `passwordBase64`
  (base64-encoded) on `/password`; response = `{"success": <bool>, …authenticatedUsername}`; a
  handler-side error returns 500 (retried by ContainerSSH).

### Keep the existing fast layer

The in-process suites (`go test ./...` in both server modules, including the in-memory authentik
mock) remain the first line of defense. This framework is the **image/wire layer**: it catches
Docker packaging, env wiring, and real-on-the-wire shape that in-process tests cannot.

## Testing Decisions

- **What makes a good test here:** external behavior only, over the HTTP wire, from a real built
  image, asserted through the reference client. Tests must not reach into the servers' internals;
  the fake authentik is the only test double and it sits at the servers' upstream API boundary. Each
  assertion is independent, named, and greppable in CI logs.
- **Modules tested:** the `config-server` image, the `auth-server` image, and — transitively — the
  fake authentik's fidelity to the single endpoint the auth-server consumes. The runner itself is
  plumbing (its assertions are the tests); it is exercised by running the suite, not unit-tested.
- **Prior art:**
  - `auth-server/auth_server_test.go` — the `mockAuthentik` `net/http` handler is the direct source
    for the standalone fake authentik (filtering + pagination envelope + PATCH handling).
  - `config-server/README.md` — the existing `curl -X POST http://127.0.0.1:8080/ …` smoke example
    is the precedent for the "one request, expect output" shape; the framework promotes that ad-hoc
    check into an executable, reference-client-backed assertion.
  - Upstream ContainerSSH `internal/config/http_test.go` (config client against a live server) and
    `auth/webhook/server_test.go` (server + handler example) — the patterns for round-tripping the
    webhook over HTTP.

## Out of Scope

- **Full SSH handshake** through a real `containerssh:v0.6` binary (significantly heavier e2e;
  belongs in its own future framework).
- **Fail-closed kill tests** (e.g. terminate the config-server mid-run and assert the client's deny
  semantics) — could be a later compose stage; the deny-on-non-200 behavior is inherited from the
  reference client and covered by upstream tests.
- **TLS / mTLS** on the webhook endpoints (a `--tls` compose profile with self-signed certs is a
  clean follow-up, not v1).
- **The normalizing + fingerprint sync** (PATCH walk) — covered by the auth-server unit tests;
  exercising it against images is deferred (would need a second compose stage with
  `AUTH_SERVER_SYNC_INTERVAL` + `AUTH_SERVER_SYNC_WRITE`).
- **Real authentik integration** — the fake covers only the `GET /api/v3/core/users/` surface the
  auth-server actually calls.
- **Helm chart / rendered-config validation** (`--dump-config` against the real binary) — a separate
  validation, already documented in AGENTS.md, not part of this framework.
- **The authz group gate** (`AUTH_SERVER_REQUIRE_GROUP` + `groups_by_name`) — only if a fixture
  stage adds it later; the base matrix's `/authz` check returns success without hitting authentik.

## Further Notes

- **CI wiring:** once the repo is pushed, extend the two image workflows (or add one matrix step)
  to run this compose as a gate, turning "image built" into "image built **and speaks the
  contract**." The runner's greppable `CONTRACT OK/FAIL` lines make CI triage cheap.
- **Fixture provenance:** derivation of the one hardcoded fingerprint constant (`ssh-keygen -lf` on
  the committed test key) should be captured as a comment in the fixture so future maintainers can
  regenerate or extend the seed set.
- **Triage status:** per request, this spec was written to a local file rather than published to an
  issue tracker. If tracking is desired later, publish it and apply the `ready-for-agent` label
  (issue-tracker/triage vocabulary should come from running `/setup-matt-pocock-skills`).
- **Suggested first slice** (to get something green fastest): the config-server half of the matrix
  (the purest "single request → output" case), then the fake authentik + auth-server pubkey paths,
  then the Dockerfiles and compose around both.
