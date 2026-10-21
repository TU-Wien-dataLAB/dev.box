#!/usr/bin/env bash
# End-to-end delivery test for user-facing auth denial messages (fork PR #10).
#
# Runs the real ContainerSSH image with a stub auth webhook and asserts, through
# a real OpenSSH client, that:
#
#   1. an authz policy denial carrying a message (the bundled auth-server's
#      unknown-template case) reaches the client as an SSH user authentication
#      banner immediately before the Permission denied failure;
#   2. a message-bearing public-key denial does the same;
#   3. a message-less denial behaves exactly as before: plain Permission denied
#      with no banner.
#
# The stub mirrors the bundled auth-server contract: public key auth succeeds
# with the enrolled owner, authz gates the SSH username against the template
# allowlist, and password always denies. Only the webhook JSON differs per user.
#
# Usage:
#   tests/gateway-error-messages.sh                    # deployed pr-10 image
#   CONTAINERSSH_IMAGE=<ref> tests/gateway-error-messages.sh
#
# Requires: docker, ssh, ssh-keygen, python3. No cluster access needed.
set -euo pipefail

image="${CONTAINERSSH_IMAGE:-ghcr.io/tu-wien-datalab/containerssh:pr-10@sha256:96519c315ae7810931df2c8b5a974750d3f8fb6007c0aad4fa9b2e6b68394a4d}"
stub_image="${STUB_IMAGE:-python:3.12-alpine}"
ssh_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
suffix="gwmsg-$$"

tmp="$(mktemp -d)"
network="devbox-$suffix"
stub_cid=""
gateway_cid=""

cleanup() {
  [ -n "$gateway_cid" ] && docker rm -f "$gateway_cid" >/dev/null 2>&1 || true
  [ -n "$stub_cid" ] && docker rm -f "$stub_cid" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# ---------------------------------------------------------------------------
# Stub auth webhook (mirrors the bundled auth-server's contract + messages).
# ---------------------------------------------------------------------------
cat >"$tmp/stub.py" <<'PYEOF'
#!/usr/bin/env python3
"""Stub ContainerSSH auth webhook: policy decisions with user-facing messages.

Usernames:
  banner-user  - authenticates, authz denies with a message (unknown template)
  keyless-user - pubkey denies with a message (key not enrolled)
  plain-user   - authenticates, authz denies without a message
  anything else - full success
"""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer


def pubkey(username):
    if username == "keyless-user":
        return {"success": False,
                "message": "public key is not enrolled for any user"}
    return {"success": True, "authenticatedUsername": "stub-owner"}


def authz(username):
    if username == "banner-user":
        return {"success": False,
                "message": "no pod template matches SSH username '%s'" % username}
    if username == "plain-user":
        return {"success": False}
    return {"success": True}


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length) or b"{}")
        username = request.get("username", "")
        path = self.path.rsplit("/", 1)[-1]
        if path == "pubkey":
            response = pubkey(username)
        elif path == "authz":
            response = authz(username)
        else:  # password and anything unknown: never accepted
            response = {"success": False}
        body = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        print("REQUEST %s username=%s response=%s"
              % (path, username, body.decode()), flush=True)

    def log_message(self, *_):
        pass


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
PYEOF

cat >"$tmp/config.yaml" <<YAMLEOF
ssh:
  listen: 0.0.0.0:2222
auth:
  publicKey:
    method: webhook
    webhook:
      url: http://stub:8080
      timeout: 2s
  authz:
    method: webhook
    webhook:
      url: http://stub:8080
      timeout: 2s
log:
  level: 6
YAMLEOF

ssh-keygen -q -t ed25519 -N "" -f "$tmp/id_ed25519" -C gwmsg-test

echo "==> image:   $image"
echo "==> network: $network (ssh port $ssh_port)"

docker network create "$network" >/dev/null

stub_cid=$(docker run -d --rm --name "devbox-$suffix-stub" --network "$network" \
  --network-alias stub \
  -v "$tmp/stub.py:/stub.py:ro" \
  "$stub_image" python3 /stub.py)
gateway_cid=$(docker run -d --rm --name "devbox-$suffix-gateway" --network "$network" \
  -v "$tmp/config.yaml:/etc/containerssh/config.yaml:ro" \
  -p "127.0.0.1:$ssh_port:2222" \
  "$image" --config /etc/containerssh/config.yaml)

# Wait for the SSH listener (the gateway logs MSSHServiceAvailable once ready).
ready=""
for _ in $(seq 1 60); do
  if docker logs "$gateway_cid" 2>&1 | grep -q "SSH server running"; then
    ready=1
    break
  fi
  if [ "$(docker container inspect -f '{{.State.Running}}' "$gateway_cid")" != "true" ]; then
    docker logs "$gateway_cid" 2>&1 >&2 || true
    fail "gateway container exited during startup"
  fi
  sleep 0.5
done
[ -n "$ready" ] || fail "gateway did not become ready within 30s"

# ---------------------------------------------------------------------------
# Cases
# ---------------------------------------------------------------------------
CASE_OUTPUT=""
ssh_case() {
  # $1 = username; sets CASE_OUTPUT, CASE_RC, CASE_SECONDS.
  local username="$1" start
  start=$SECONDS
  set +e
  CASE_OUTPUT=$(ssh -i "$tmp/id_ed25519" \
    -o IdentitiesOnly=yes -o BatchMode=yes \
    -o PreferredAuthentications=publickey \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 \
    -p "$ssh_port" "$username@127.0.0.1" true 2>&1)
  CASE_RC=$?
  set -e
  CASE_SECONDS=$((SECONDS - start))
}

stub_saw() {
  docker logs "$stub_cid" 2>&1 | grep -q "REQUEST $1 username=$2 "
}

# Case 1: authz denial with a message (the unknown-template scenario).
ssh_case banner-user
output="$CASE_OUTPUT"
echo "--- banner-user (authz denial with message) ---"
echo "$output"
echo "------------------------------------------------"
[ "$CASE_RC" -eq 255 ] || fail "banner-user: expected exit 255, got $CASE_RC"
echo "$output" | grep -q "Permission denied" \
  || fail "banner-user: missing Permission denied"
echo "$output" | grep -qF "no pod template matches SSH username 'banner-user'" \
  || fail "banner-user: client did not receive the denial message as a banner"
[ "$CASE_SECONDS" -lt 5 ] \
  || fail "banner-user: denial took ${CASE_SECONDS}s (must be terminal and prompt)"
stub_saw pubkey banner-user || fail "banner-user: stub never saw the pubkey request"
stub_saw authz banner-user || fail "banner-user: stub never saw the authz request"

# Case 2: pubkey denial with a message.
ssh_case keyless-user
output="$CASE_OUTPUT"
echo "--- keyless-user (pubkey denial with message) ---"
echo "$output"
echo "------------------------------------------------"
[ "$CASE_RC" -eq 255 ] || fail "keyless-user: expected exit 255, got $CASE_RC"
echo "$output" | grep -q "Permission denied" \
  || fail "keyless-user: missing Permission denied"
echo "$output" | grep -qF "public key is not enrolled for any user" \
  || fail "keyless-user: client did not receive the denial message as a banner"
[ "$CASE_SECONDS" -lt 5 ] \
  || fail "keyless-user: denial took ${CASE_SECONDS}s (must be terminal and prompt)"
stub_saw pubkey keyless-user || fail "keyless-user: stub never saw the pubkey request"

# Case 3: message-less denial must behave exactly as before (no banner).
ssh_case plain-user
output="$CASE_OUTPUT"
echo "--- plain-user (denial without message) ---"
echo "$output"
echo "------------------------------------------------"
[ "$CASE_RC" -eq 255 ] || fail "plain-user: expected exit 255, got $CASE_RC"
echo "$output" | grep -q "Permission denied" \
  || fail "plain-user: missing Permission denied"
echo "$output" | grep -qF "no pod template matches" \
  && fail "plain-user: unexpected banner for a message-less denial" || true
stub_saw pubkey plain-user || fail "plain-user: stub never saw the pubkey request"
stub_saw authz plain-user || fail "plain-user: stub never saw the authz request"

echo "PASS: message-bearing denials reach the client as banners; message-less denials do not"
