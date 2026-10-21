#!/usr/bin/env python3
"""Docker-only checks for the settings guest image; this does not validate SSH gateway policy.

Besides the shell-free image checks, this drives the real management UI over a
PTY: rows rendered from stand-in Kubernetes API gets, delete confirmation and
cancel, the Terminating-to-not-created transition, manual refresh, the honest
Unreachable degradation without any API, and degraded terminals.
"""

import argparse
import errno
import fcntl
import hashlib
import json
import os
import pty
import re
import select
import signal
import struct
import subprocess
import termios
import time
import urllib.request
import uuid

APP = "/usr/local/bin/dev-box-settings"
AGENT = "/usr/bin/containerssh-agent"
OWNER_ENV = "DEV_BOX_AUTHENTICATED_OWNER"
TEMPLATES_ENV = "DEV_BOX_BOX_TEMPLATES"
ANSI = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
TIMEOUT = 10
POLL_TIMEOUT = 8  # the UI refreshes every ~3s; allow a full poll cycle plus slack


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def box_name(owner, template):
    return "box-" + hashlib.sha256(f"{owner}\x00{template}".encode()).hexdigest()[:10]


class Terminal:
    """A PTY attached to the Docker CLI, not an application/model test hook."""

    def __init__(self, command, cols=80, rows=24):
        self.master, slave = pty.openpty()
        self.output = bytearray()
        self.original = termios.tcgetattr(self.master)
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        try:
            self.process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave)
        except BaseException:
            os.close(self.master)
            raise
        finally:
            os.close(slave)

    def read(self, timeout=0.05):
        if select.select([self.master], [], [], timeout)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                return
            self.output.extend(data)
            require(len(self.output) < 1024 * 1024, "unbounded terminal output")

    def text(self):
        return ANSI.sub(b"", bytes(self.output)).decode("utf-8", "replace")

    def expect(self, *fragments, timeout=POLL_TIMEOUT):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.read()
            if all(fragment in self.text() for fragment in fragments):
                return
            require(self.process.poll() is None, f"terminal exited before {fragments!r}: {self.text()!r}")
        raise AssertionError(f"terminal did not show {fragments!r}: {self.text()!r}")

    def send(self, data):
        os.write(self.master, data)

    def stays_running(self):
        deadline = time.monotonic() + 0.25
        while time.monotonic() < deadline:
            self.read()
            require(self.process.poll() is None, f"input escaped/exited the application: {self.text()!r}")

    def resize(self, cols, rows):
        self.output.clear()
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        # The Docker CLI forwards SIGWINCH and the new host PTY dimensions.
        os.kill(self.process.pid, signal.SIGWINCH)

    def exit(self, code=0):
        deadline = time.monotonic() + TIMEOUT
        while self.process.poll() is None and time.monotonic() < deadline:
            self.read()
        require(self.process.poll() == code, f"expected exit {code}, got {self.process.poll()}: {self.text()!r}")
        self.read(0)
        require(termios.tcgetattr(self.master) == self.original, "client terminal settings were not restored")

    def __enter__(self):
        return self

    def __exit__(self, *_):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=TIMEOUT)
        os.close(self.master)


# Stand-in Kubernetes pod API served from a scratch-adjacent container; the
# settings image reaches it over a private docker network (never the internet).
FAKE_API = r'''
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN = "@token@"
NAMESPACE = "containerssh-sessions"
LOCK = threading.Lock()
PODS = @pods@
GETS = [0]


def status(handler, code, reason, message):
    body = {"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": code}
    if reason:
        body["reason"] = reason
    if message:
        body["message"] = message
    handler.send_response(code)
    handler.send_header("Content-Type", "application/json")
    handler.end_headers()
    handler.wfile.write(json.dumps(body).encode())


def pod(handler, doc):
    pod = {
        "metadata": {"name": doc["name"]},
        "spec": {
            "runtimeClassName": doc.get("runtimeClass"),
            "containers": [{
                "name": "shell",
                "image": doc.get("image", "containerssh/containerssh-guest-image"),
                "resources": {"requests": doc.get("requests", {}), "limits": doc.get("limits", {})},
            }],
        },
        "status": {
            "phase": doc.get("phase", "Running"),
            "startTime": time.strftime(
                "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - doc.get("startedAgo", 7200))
            ),
            "containerStatuses": [{"restartCount": doc.get("restarts", 0)}],
        },
    }
    if doc.get("terminating"):
        pod["metadata"]["deletionTimestamp"] = time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 1)
        )
    handler.send_response(200)
    handler.send_header("Content-Type", "application/json")
    handler.end_headers()
    handler.wfile.write(json.dumps(pod).encode())


class Handler(BaseHTTPRequestHandler):
    def reply(self):
        parts = self.path.strip("/").split("/")
        if parts[0] == "_admin":
            # Host-side test tooling, not part of the pod API surface.
            with LOCK:
                if self.command == "POST":
                    body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
                    action, name = body.get("action"), body.get("name")
                    if action == "gone":
                        PODS.pop(name, None)
                    elif action == "add":
                        PODS[name] = body["doc"]
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(b"ok")
                    return
                if self.command == "GET" and "gets" in self.path:
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(str(GETS[0]).encode())
                    return
            status(self, 404, "NotFound", "not found")
            return
        if TOKEN and self.headers.get("Authorization") != f"Bearer {TOKEN}":
            status(self, 401, "Unauthorized", "credentials rejected")
            return
        if parts[:4] == ["api", "v1", "namespaces", NAMESPACE] and len(parts) == 6 and parts[4] == "pods":
            name = parts[5]
            with LOCK:
                doc = PODS.get(name)
                if self.command == "GET":
                    GETS[0] += 1
                    if doc is None:
                        status(self, 404, "NotFound", f'pods "{name}" not found')
                    else:
                        pod(self, doc)
                elif self.command == "DELETE":
                    if doc is None:
                        status(self, 404, "NotFound", f'pods "{name}" not found')
                    else:
                        doc["terminating"] = True
                        status(self, 200, "", "")
                else:
                    status(self, 405, "NotAllowed", "method not allowed")
            return
        status(self, 404, "NotFound", "not found")

    do_GET = do_POST = do_DELETE = reply

    def log_message(self, *_):
        pass


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="locally built linux/amd64 settings image")
    args = parser.parse_args()
    names = []
    networks = []

    def container_name():
        name = "dev-box-settings-smoke-" + uuid.uuid4().hex[:12]
        names.append(name)
        return name

    sandbox = ["--platform", "linux/amd64", "--network", "none", "--read-only",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges"]

    def run_args(owner="alice.example", extra=()):
        command = ["docker", "run", "--rm", "--name", container_name()] + sandbox
        command += ["-e", "TERM=xterm-256color"]
        if owner is not None:
            command += ["-e", f"{OWNER_ENV}={owner}"]
        return command + list(extra)

    def docker(*command, check=True):
        result = subprocess.run(["docker", *command], capture_output=True, timeout=TIMEOUT, check=check)
        return result.stdout.decode().strip()

    try:
        image = json.loads(docker("image", "inspect", args.image))[0]
        require(image["Architecture"] == "amd64" and image["Os"] == "linux", "expected linux/amd64 image")
        require(image["Config"]["User"] == "1000:1000", "image must default to non-root 1000:1000")
        require(image["Config"]["Entrypoint"] == [APP], "entrypoint must directly launch the absolute application")
        print(f"Image: {image['Id']}")

        with Terminal(run_args() + ["-it", args.image]) as terminal:
            terminal.expect("dev.box settings", "Owner: alice.example", "Connection:", "q: quit")
            for data in (b"\x1a", b"\x1b", b"\x04", b"\x1bq",
                         b"\r/bin/sh\rprintf 'DEV_BOX_%s' ESCAPE_EXECUTED\r"):
                terminal.send(data)
                terminal.stays_running()
            require("DEV_BOX_ESCAPE_EXECUTED" not in terminal.text(), "typed command was executed")
            terminal.resize(24, 5)
            terminal.expect("settings", "Owner: alice", "No box templates", "q: quit")
            terminal.resize(12, 3)
            terminal.expect("settings", "q: quit")
            terminal.resize(100, 24)
            terminal.expect("dev.box settings", "Owner: alice.example", "No box templates", "q: quit")
            terminal.send(b"q")
            terminal.exit()
        print("PASS: terminal render, inert commands/Ctrl-Z/ESC, small resize, q, terminal restoration")

        no_pty = subprocess.run(run_args() + [args.image], input=b"", capture_output=True, timeout=TIMEOUT)
        require(no_pty.returncode == 1 and b"PTY" in no_pty.stderr and b"ssh -t" in no_pty.stderr,
                f"unhelpful/unbounded no-PTY failure: {no_pty!r}")
        for owner in (None, "", " \t\r\n"):
            with Terminal(run_args(owner) + ["-it", args.image]) as terminal:
                terminal.exit(1)
                require(OWNER_ENV in terminal.text() and "missing" in terminal.text(), "missing identity must fail closed")
                require("q: quit" not in terminal.text(), "missing identity opened UI")
        print("PASS: bounded helpful no-PTY and unset/empty/whitespace trusted-owner failures")

        hostile_owner = "Björk.李\x1b]52;c;clipboard\a\x1b[31m\r\nINJECTED\u009b32m\u202ebidi\u2028line"
        with Terminal(run_args(hostile_owner, ["-e", "NO_COLOR=1"]) + ["-it", args.image], cols=160) as terminal:
            terminal.expect("Owner: Björk.李", "q: quit")
            for control in ("\x1b]52;c;clipboard", "\a", "\x1b[31m", "\r\nINJECTED", "\x9b", "\u202e", "\u2028"):
                require(control.encode() not in terminal.output, "owner injected terminal control")
            terminal.send(b"q")
            terminal.exit()
        print("PASS: Unicode owner displayed as inert text, no control-sequence injection")

        shell = subprocess.run(run_args() + ["--entrypoint", "/bin/sh", args.image, "-c", "exit 0"],
                               capture_output=True, timeout=TIMEOUT)
        require(shell.returncode != 0 and b"no such file or directory" in shell.stderr, "image exposes /bin/sh")
        print("PASS: shell-free image and direct non-root entrypoint")

        # A malformed trusted template list fails closed before the UI opens.
        for templates in ("bad name", "a/b"):
            with Terminal(run_args(extra=["-e", f"{TEMPLATES_ENV}={templates}"]) + ["-it", args.image]) as terminal:
                terminal.exit(1)
                require(TEMPLATES_ENV in terminal.text() and "invalid template name" in terminal.text(),
                        "malformed trusted template list must fail closed")
        with Terminal(run_args(extra=["-e", f"{TEMPLATES_ENV}="]) + ["-it", args.image]) as terminal:
            terminal.expect("No box templates are available.", "q: quit")
            terminal.send(b"d")
            terminal.expect("nothing to delete")
            terminal.send(b"q")
            terminal.exit()
        print("PASS: trusted template list validated; empty list renders the empty state")

        # Management against a stand-in Kubernetes API on a private docker
        # network: the image has no internet access, only the fake API. The
        # helper image is unpinned test tooling; it handles only test data.
        network = "dev-box-settings-smoke-" + uuid.uuid4().hex[:8]
        networks.append(network)
        docker("network", "create", network)
        owner = "alice.example"
        ubuntu, data = box_name(owner, "ubuntu"), box_name(owner, "data-engine")
        fake_api_path = f"/tmp/dev-box-settings-smoke-{uuid.uuid4().hex[:8]}.py"
        with open(fake_api_path, "w") as handle:
            handle.write(FAKE_API.replace("@token@", "smoke-token").replace(
                "@pods@", json.dumps({ubuntu: {"name": ubuntu, "phase": "Running", "startedAgo": 7200,
                                               "restarts": 1, "runtimeClass": "kata-qemu-runtime-rs",
                                               "image": "containerssh/containerssh-guest-image",
                                               "requests": {"cpu": "250m", "memory": "512Mi"},
                                               "limits": {"cpu": "500m", "memory": "1Gi"}}})))
        os.chmod(fake_api_path, 0o600)
        try:
            fake = container_name()
            docker("run", "-d", "--rm", "--name", fake, "--network", network,
                   "--network-alias", "fake",
                   "-p", "127.0.0.1::8080", "-v", f"{fake_api_path}:/srv/fake.py:ro", "--read-only",
                   "python:3-alpine", "python", "/srv/fake.py")
            published = docker("port", fake, "8080/tcp")
            host_port = published.split(":")[-1]
            require(host_port.isdigit(), f"fake API port not published: {published!r}")

            def api_env(templates="ubuntu,data-engine", token="smoke-token"):
                env = ["-e", "NO_COLOR=1",
                       "-e", f"{TEMPLATES_ENV}={templates}",
                       "-e", "DEV_BOX_MAX_BOXES=2",
                       "-e", "DEV_BOX_KUBE_API_URL=http://fake:8080",
                       "-e", "DEV_BOX_NAMESPACE=containerssh-sessions"]
                if token is not None:
                    env += ["-e", f"DEV_BOX_KUBE_API_TOKEN={token}"]
                return env

            def api_run(templates="ubuntu,data-engine", token="smoke-token", cols=140):
                return (f"http://127.0.0.1:{host_port}",
                        ["docker", "run", "--rm", "-it", "--name", container_name(),
                         "--platform", "linux/amd64", "--network", network, "--read-only",
                         "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                         "-e", "TERM=xterm-256color", "-e", f"{OWNER_ENV}={owner}"]
                        + api_env(templates, token) + [args.image], cols)

            def api_admin(base, action, name=None, doc=None):
                body = json.dumps({"action": action, "name": name, "doc": doc}).encode()
                request = urllib.request.Request(f"{base}/_admin", data=body, method="POST")
                with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                    require(response.read() == b"ok", "fake API admin call failed")

            def api_gets(base):
                with urllib.request.urlopen(f"{base}/_admin/gets", timeout=TIMEOUT) as response:
                    return int(response.read())

            # The management flow: master rows from gets, selection via the
            # detail pane, confirm/cancel, refresh, Terminating, not-created,
            # the box-cap line, quit.
            base, command, cols = api_run()
            with Terminal(command, cols=cols) as terminal:
                # The sorted master list starts at data-engine (not created);
                # its detail pane names the create command, and the master
                # pane foots the box cap.
                terminal.expect(
                    "Owner: alice.example", "Live",
                    "data-engine", "not created",
                    "ubuntu", "Running",
                    "ssh data-engine@<host> creates this box",
                    "created 1/2",
                )
                # Selection and cancel: the initially selected data-engine is
                # not created, so d explains instead of deleting.
                terminal.send(b"d")
                terminal.expect("data-engine is not created")
                terminal.send(b"j")   # select ubuntu: strip + ledger detail
                terminal.expect("up 2h", "RESTARTS",
                                "containerssh/containerssh-guest-image", "kata-qemu-runtime-rs",
                                "REQUESTS", "512Mi/250m", "LIMITS", "1Gi/500m")
                terminal.send(b"d")
                terminal.expect("Delete box \"ubuntu\"", "all data on the box's local disk is destroyed",
                                "active sessions in the box are killed",
                                "a fresh box is created on your next login:",
                                "ssh ubuntu@<host>")
                terminal.send(b"n")
                terminal.stays_running()
                # Manual refresh issues new gets.
                before = api_gets(base)
                terminal.send(b"r")
                deadline = time.monotonic() + TIMEOUT
                while time.monotonic() < deadline and api_gets(base) < before + 2:
                    time.sleep(0.05)
                require(api_gets(base) >= before + 2, "manual refresh did not issue new gets")
                # Confirmed delete: Terminating, then the not-created row.
                terminal.send(b"d")
                terminal.expect("Delete box \"ubuntu\"")
                terminal.send(b"y")
                terminal.expect("Terminating")
                api_admin(base, "gone", name=ubuntu)
                terminal.output.clear()
                # The differential renderer may repaint only changed cells, so
                # assert the redrawn count, not the contiguous "created 0/2".
                terminal.expect("ssh ubuntu@<host> creates this box", "0/2")
                terminal.send(b"q")
                terminal.exit()
            print("PASS: management master/detail, selection, confirm/cancel, refresh, Terminating, recreate hint, box cap")

            def seed_ubuntu(base):
                api_admin(base, "add", name=ubuntu, doc={
                    "name": ubuntu, "phase": "Running", "startedAgo": 7200, "restarts": 1,
                    "runtimeClass": "kata-qemu-runtime-rs",
                    "image": "containerssh/containerssh-guest-image",
                    "requests": {"cpu": "250m", "memory": "512Mi"},
                    "limits": {"cpu": "500m", "memory": "1Gi"}})

            # Dumb terminal: plain text, no SGR colors, still fully usable.
            base, command, cols = api_run()
            seed_ubuntu(base)
            term_index = command.index("TERM=xterm-256color")
            command[term_index] = "TERM=dumb"
            with Terminal(command, cols=cols) as terminal:
                terminal.expect("Running", "Owner: alice.example", "Live")
                for sgr in (b"\x1b[31m", b"\x1b[32m", b"\x1b[33m", b"\x1b[2m", b"\x1b[1m"):
                    require(sgr not in terminal.output, f"dumb terminal received color sequence {sgr!r}")
                terminal.send(b"q")
                terminal.exit()
            print("PASS: dumb terminal renders plain text without color sequences")

            # A wrong or missing token keeps the UI honest: Unreachable, delete refused.
            for token in (None, "wrong-token"):
                base, command, cols = api_run(token=token)
                with Terminal(command, cols=cols) as terminal:
                    terminal.expect("Unreachable", "unreachable", "q: quit")
                    terminal.send(b"d")
                    terminal.expect("delete is unavailable")
                    terminal.send(b"q")
                    terminal.exit()
            print("PASS: rejected credentials degrade to Unreachable with delete disabled")
        finally:
            os.unlink(fake_api_path)

        # No API at all (network none): the UI stays up and honest.
        with Terminal(run_args(extra=["-e", f"{TEMPLATES_ENV}=ubuntu", "-e", "NO_COLOR=1"]) + ["-it", args.image]) as terminal:
            terminal.expect("Unreachable", "unreachable", "q: quit")
            terminal.send(b"d")
            terminal.expect("delete is unavailable")
            terminal.send(b"q")
            terminal.exit()
        print("PASS: no in-cluster API degrades to Unreachable without crashing")

        for sig in ("TERM", "INT"):
            idle = container_name()
            docker("run", "-d", "--name", idle, *sandbox, "--entrypoint", AGENT, args.image,
                   "wait-signal", "--signal", "INT", "--signal", "TERM")
            # The official agent has no readiness output (--message prints on exit).
            # Give its signal handlers time to install, as its own tests do.
            time.sleep(1)
            require(docker("inspect", "--format", "{{.State.Running}}", idle) == "true",
                    "upstream wait-signal command exited before receiving a signal")
            if sig == "TERM":
                # Connection-mode ContainerSSH exec: console, PID record, env, absolute executable.
                console = ["docker", "exec", "-it", idle, AGENT, "console", "--pid",
                           "--env", f"{OWNER_ENV}=agent.owner", "--env", "TERM=xterm-256color", "--", APP]
                with Terminal(console) as terminal:
                    terminal.expect("dev.box settings", "Owner: agent.owner", "q: quit")
                    pid = struct.unpack("<I", terminal.output[:4])[0]
                    require(0 < pid < 4194304, "agent did not emit its little-endian PID record")
                    terminal.send(b"q")
                    terminal.exit()
                require(docker("inspect", "--format", "{{.State.Running}}", idle) == "true",
                        "console exit killed idle agent")
                print("PASS: agent console --pid/--env launches the app; q leaves idle agent running")
            docker("kill", "--signal", sig, idle)
            require(docker("wait", idle) == "0", f"wait-signal did not exit cleanly on {sig}")
            print(f"PASS: upstream idle wait-signal command handles {sig}")
    finally:
        for name in names:
            subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=TIMEOUT)
        for network in networks:
            subprocess.run(["docker", "network", "rm", network], capture_output=True, timeout=TIMEOUT)


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit(f"FAIL: {error}") from error
