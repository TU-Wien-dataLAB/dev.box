#!/usr/bin/env python3
"""Opt-in live Settings checks: killed OpenSSH client and actual control master.

Uses existing enrolled credentials; never deploys, deletes pods, or changes auth.
Requires approval for real SSH connections and read-only cluster observations.
"""
import argparse
import contextlib
import fcntl
import json
import os
import pathlib
import pty
import re
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def passed(message):
    print("PASS: " + message, flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="localhost")
    parser.add_argument("--port", type=int, default=2222)
    parser.add_argument("--key", type=pathlib.Path, required=True)
    parser.add_argument("--known-hosts", type=pathlib.Path, required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--kube-context", required=True)
    parser.add_argument("--session-namespace", required=True)
    parser.add_argument("--ordinary-pod", required=True)
    parser.add_argument("--settings-selector", required=True)
    parser.add_argument("--timeout", type=int, default=90)
    args = parser.parse_args()
    require(1 <= args.port <= 65535 and 1 <= args.timeout <= 300, "invalid port/timeout")
    for value in (args.host, args.kube_context, args.session_namespace, args.ordinary_pod, args.settings_selector):
        require(value.strip() and value == value.strip(), "host/context/namespace/pod/selector must be explicit nonempty values")
    require(args.owner.strip() and args.owner.isprintable(), "owner must be nonempty printable text")
    for path in (args.key, args.known_hosts):
        require(path.expanduser().is_file(), "missing key/known-hosts file")

    def alarm(*_):
        raise AssertionError("total lifecycle probe deadline exceeded")

    signal.signal(signal.SIGALRM, alarm)
    signal.alarm(args.timeout * 5)
    base = ["ssh", "-F", "/dev/null", "-p", str(args.port), "-i", str(args.key.expanduser()),
            "-o", "IdentitiesOnly=yes", "-o", "PreferredAuthentications=publickey",
            "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
            "-o", "UserKnownHostsFile=" + str(args.known_hosts.expanduser()),
            "-o", "GlobalKnownHostsFile=/dev/null", "-o", "ConnectTimeout=15"]
    target = "settings@" + args.host

    def command(*parts):
        return subprocess.run(parts, capture_output=True, timeout=args.timeout, text=True)

    def kube(*parts):
        result = command("kubectl", "--context", args.kube_context, "-n", args.session_namespace,
                         "--request-timeout=15s", *parts, "-o", "json")
        require(result.returncode == 0, "kubectl observation failed: " + result.stderr[:1000])
        return json.loads(result.stdout) if result.stdout.strip() else None

    def settings():
        return {p["metadata"]["uid"]: p for p in kube("get", "pods", "-l", args.settings_selector)["items"]}

    def existing_ordinary_uid():
        pod = kube("get", "pod", args.ordinary_pod, "--ignore-not-found")
        require(pod is not None and not pod["metadata"].get("deletionTimestamp"),
                "ordinary pod missing/terminating; do not reconnect and recreate it")
        return pod["metadata"]["uid"]

    def ordinary():
        require(existing_ordinary_uid() == ordinary_uid, "ordinary UID changed; do not reconnect")
        result = command(*base, "-S", "none", "ubuntu@" + args.host, "printf ORDINARY_LIFECYCLE_OK")
        require(result.returncode == 0 and result.stdout == "ORDINARY_LIFECYCLE_OK", "ordinary SSH control failed")
        return existing_ordinary_uid()

    observed_names = {}

    def fresh(baseline):
        current = settings()
        new = set(current) - baseline
        require(len(new) == 1, "expected exactly one fresh test pod; stop on concurrent activity")
        uid = new.pop()
        pod = current[uid]
        require(not pod["metadata"].get("deletionTimestamp"), "new pod already terminating")
        observed_names[uid] = pod["metadata"]["name"]
        print("OBSERVED: " + json.dumps({"pod": pod["metadata"]["name"], "uid": uid}), flush=True)
        return uid

    def gone(uid):
        end = time.monotonic() + args.timeout
        while True:
            pod = kube("get", "pod", observed_names[uid], "--ignore-not-found")
            if pod is None or pod["metadata"]["uid"] != uid:
                break  # Original UID is absent, not merely hidden by a label change.
            require(time.monotonic() < end, "test pod cleanup exceeded bound: " + uid)
            time.sleep(0.2)
        passed("test pod removed after full disconnect: " + uid)

    @contextlib.contextmanager
    def terminal(parts):
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        child = None
        try:
            child = subprocess.Popen(parts, stdin=slave, stdout=slave, stderr=slave,
                                     start_new_session=True,
                                     preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0))
            output = bytearray()
            end = time.monotonic() + args.timeout
            while True:
                if select.select([master], [], [], 0.1)[0]:
                    output.extend(os.read(master, 8192))
                require(len(output) <= 262144, "terminal output exceeds cap")
                text = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", output).decode("utf-8", "replace")
                if ("Settings" in text and "read-only" in text and "q: quit" in text and
                        re.search(r"\bOwner:\s*" + re.escape(args.owner) + r"(?=\s|$)", text)):
                    break
                require(child.poll() is None, "SSH exited before canonical owner screen")
                require(time.monotonic() < end, "Settings screen timeout")
            yield child, master
        finally:
            if child is not None:
                if child.poll() is None:
                    child.kill()  # only this probe's local child, never a remote process/pod
                child.wait(timeout=10)
            os.close(master)
            os.close(slave)

    baseline = set(settings())
    ordinary_uid = existing_ordinary_uid()  # Snapshot BEFORE SSH can create anything.
    require(ordinary() == ordinary_uid, "ordinary UID changed during initial SSH control")
    print("OBSERVED: ordinary UID " + ordinary_uid, flush=True)
    try:
        # SIGKILL gives OpenSSH no opportunity for orderly SSH/channel shutdown.
        uid = None
        try:
            with terminal(base + ["-S", "none", "-tt", target]) as (client, _):
                uid = fresh(baseline)
                os.kill(client.pid, signal.SIGKILL)
                require(client.wait(timeout=10) == -signal.SIGKILL, "test SSH client was not killed")
                passed("live Settings OpenSSH client terminated by SIGKILL")
        finally:
            if uid:
                gone(uid)
        require(ordinary() == ordinary_uid, "ordinary UID changed after abrupt disconnect")

        with tempfile.TemporaryDirectory(prefix="db-mux-", dir="/tmp") as directory:
            socket = directory + "/master"
            uid = None
            with open(directory + "/master.log", "w+") as log:
                parent = subprocess.Popen(base + ["-M", "-N", "-S", socket, "-o", "ControlPersist=no", target],
                                          stdin=subprocess.DEVNULL, stdout=log, stderr=log)
                try:
                    end = time.monotonic() + args.timeout
                    while not os.path.exists(socket):
                        require(parent.poll() is None, "OpenSSH control master exited")
                        require(time.monotonic() < end, "control socket creation timeout")
                        time.sleep(0.1)
                    with terminal(base + ["-S", socket, "-tt", target]) as (client, master):
                        uid = fresh(baseline)
                        os.write(master, b"q")
                        end = time.monotonic() + args.timeout
                        while client.poll() is None and time.monotonic() < end:
                            if select.select([master], [], [], 0.1)[0]:
                                try:
                                    os.read(master, 8192)
                                except OSError:
                                    break  # macOS can revoke a session leader's PTY on exit
                        require(client.poll() == 0, "multiplexed q did not exit 0 within bound")
                    check = command(*base, "-S", socket, "-O", "check", target)
                    require(check.returncode == 0 and parent.poll() is None, "master died after q")
                    retained = settings().get(uid)
                    require(retained and not retained["metadata"].get("deletionTimestamp"),
                            "pod removed before control-master transport ended")
                    # OpenSSH otherwise falls back to a NEW TCP connection after
                    # mux refusal. Make that fallback fail locally, not reach SSH.
                    second = command(*base, "-S", socket, "-o", "ProxyCommand=false", "-T", target)
                    require(second.returncode == 255 and "session open refused by peer" in second.stderr.lower(),
                            "second mux session lacked explicit refusal: " + second.stderr[:1000])
                    require(command(*base, "-S", socket, "-O", "check", target).returncode == 0,
                            "second-session refusal killed control master")
                    passed("actual OpenSSH master retains pod after q and denies second session while healthy")
                    require(command(*base, "-S", socket, "-O", "exit", target).returncode == 0,
                            "could not terminate this probe's control master")
                    status = parent.wait(timeout=10)
                    # A -N master has no remote command exit status and can return
                    # 255 after an acknowledged -O exit. Require socket shutdown
                    # and pod cleanup rather than inventing a remote exit code.
                    require(status in (0, 255), "unexpected master termination status: " + str(status))
                    require(command(*base, "-S", socket, "-O", "check", target).returncode != 0,
                            "control master still reachable after acknowledged exit")
                    passed("control-master exit acknowledged; local -N status " + str(status))
                finally:
                    try:
                        if parent.poll() is None:
                            parent.terminate()
                            try:
                                parent.wait(timeout=5)
                            except subprocess.TimeoutExpired:
                                parent.kill()
                                parent.wait(timeout=5)
                    finally:
                        if uid:
                            gone(uid)
    finally:
        require(ordinary() == ordinary_uid, "ordinary UID changed during lifecycle probes")
    require(set(settings()) == baseline, "Settings pod baseline changed; inspect, do not delete broadly")
    signal.alarm(0)
    passed("abrupt disconnect and actual OpenSSH multiplexed lifetime; ordinary UID and Settings baseline preserved")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, ValueError, subprocess.SubprocessError) as error:
        print("FAIL: " + str(error), flush=True)
        raise SystemExit(1)
