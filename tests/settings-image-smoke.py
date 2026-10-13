#!/usr/bin/env python3
"""Docker-only checks for issue #9; this does not validate SSH gateway policy."""

import argparse
import errno
import fcntl
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
import uuid

APP = "/usr/local/bin/dev-box-settings"
AGENT = "/usr/bin/containerssh-agent"
OWNER_ENV = "DEV_BOX_AUTHENTICATED_OWNER"
ANSI = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
TIMEOUT = 10


def require(condition, message):
    if not condition:
        raise AssertionError(message)


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

    def expect(self, *fragments):
        deadline = time.monotonic() + TIMEOUT
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="locally built linux/amd64 settings image")
    args = parser.parse_args()
    names = []

    def container_name():
        name = "dev-box-settings-smoke-" + uuid.uuid4().hex[:12]
        names.append(name)
        return name

    sandbox = ["--platform", "linux/amd64", "--network", "none", "--read-only",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges"]

    def run_args(owner="alice.example"):
        command = ["docker", "run", "--rm", "--name", container_name()] + sandbox
        command += ["-e", "TERM=xterm-256color"]
        if owner is not None:
            command += ["-e", f"{OWNER_ENV}={owner}"]
        return command

    def docker(*command):
        result = subprocess.run(["docker", *command], capture_output=True, timeout=TIMEOUT, check=True)
        return result.stdout.decode().strip()

    try:
        image = json.loads(docker("image", "inspect", args.image))[0]
        require(image["Architecture"] == "amd64" and image["Os"] == "linux", "expected linux/amd64 image")
        require(image["Config"]["User"] == "1000:1000", "image must default to non-root 1000:1000")
        require(image["Config"]["Entrypoint"] == [APP], "entrypoint must directly launch the absolute application")
        print(f"Image: {image['Id']}")

        with Terminal(run_args() + ["-it", args.image]) as terminal:
            terminal.expect("Settings", "POC", "read-only", "Owner: alice.example", "q: quit")
            for data in (b"\x1a", b"\x1b", b"\x03\x04", b"\x1bq",
                         b"\r/bin/sh\rprintf 'DEV_BOX_%s' ESCAPE_EXECUTED\r"):
                terminal.send(data)
                terminal.stays_running()
            require("DEV_BOX_ESCAPE_EXECUTED" not in terminal.text(), "typed command was executed")
            terminal.resize(24, 5)
            terminal.expect("Settings", "read-only", "Owner: alice.example", "q: quit")
            terminal.resize(12, 3)
            terminal.expect("Settings", "POC/RO", "q: quit")
            terminal.resize(100, 24)
            terminal.expect("Settings", "read-only", "Owner: alice.example", "q: quit")
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
        with Terminal(run_args(hostile_owner) + ["-it", args.image], cols=160) as terminal:
            terminal.expect("Owner: Björk.李", "q: quit")
            for control in ("\x1b]52;c;clipboard", "\a", "\x1b[31m", "\r\nINJECTED", "\u009b", "\u202e", "\u2028"):
                require(control.encode() not in terminal.output, "owner injected terminal control")
            terminal.send(b"q")
            terminal.exit()
        print("PASS: Unicode owner displayed as inert text, no control-sequence injection")

        shell = subprocess.run(run_args() + ["--entrypoint", "/bin/sh", args.image, "-c", "exit 0"],
                               capture_output=True, timeout=TIMEOUT)
        require(shell.returncode != 0 and b"no such file or directory" in shell.stderr, "image exposes /bin/sh")
        print("PASS: shell-free image and direct non-root entrypoint")

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
                    terminal.expect("Settings", "read-only", "Owner: agent.owner", "q: quit")
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


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit(f"FAIL: {error}") from error
