#!/usr/bin/env python3
"""Bounded, opt-in SSH acceptance probe for issue #9 (never deploys anything).

Requires Paramiko 3.5.1; see tests/settings-acceptance.md. Every invocation with
connection arguments makes real SSH connections and may create backend pods.
"""
import argparse
import contextlib
import getpass
import json
import pathlib
import re
import signal
import subprocess
import sys
import threading
import time
import uuid


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def passed(message):
    print(f"PASS: {message}", flush=True)


def arguments():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--host", required=True)
    p.add_argument("--port", required=True, type=int)
    p.add_argument("--key", required=True, type=pathlib.Path)
    p.add_argument("--owner", required=True, help="expected canonical authentik owner, not SSH username")
    p.add_argument("--second-key", type=pathlib.Path)
    p.add_argument("--second-owner")
    p.add_argument("--known-hosts", type=pathlib.Path, default=pathlib.Path("~/.ssh/known_hosts"))
    p.add_argument("--ask-key-passphrase", action="store_true")
    p.add_argument("--timeout", type=float, default=60, help="per-operation bound in seconds (default 60)")
    p.add_argument("--max-total-seconds", type=float, default=1200)
    p.add_argument("--kube-context", help="optional read-only lifecycle observations; never uses default context")
    p.add_argument("--session-namespace")
    p.add_argument("--ordinary-pod", help="existing ubuntu pod name; UID must remain unchanged")
    p.add_argument("--settings-selector", help="verified settings-only pod label selector, not an owner selector")
    p.add_argument("--resize-exit-stress", action="store_true",
                   help="opt-in resize/quit race checks; can crash unfixed gateways, requires live approval")
    a = p.parse_args()
    if not 1 <= a.port <= 65535 or not 0 < a.timeout <= 300 or not 0 < a.max_total_seconds <= 7200:
        p.error("port must be 1..65535; timeout 0..300 and total bound 0..7200 seconds (exclusive zero)")
    if bool(a.second_key) != bool(a.second_owner):
        p.error("--second-key and --second-owner must be supplied together")
    for owner in (a.owner, a.second_owner):
        if owner is not None and (not owner.strip() or not owner.isprintable()):
            p.error("expected owners must be nonempty printable text")
    if a.second_owner == a.owner:
        p.error("the second owner must be distinct")
    kube = (a.kube_context, a.session_namespace, a.ordinary_pod, a.settings_selector)
    if any(kube) and not all(kube):
        p.error("lifecycle observations require all four kubectl options")
    for path in filter(None, (a.key, a.second_key, a.known_hosts)):
        if not path.expanduser().is_file():
            p.error(f"file not found: {path}")
    return a


def main():
    a = arguments()  # --help works without Paramiko and without network access.
    try:
        import paramiko
        from paramiko import Message
        from paramiko.common import (
            MSG_CHANNEL_SUCCESS, MSG_CHANNEL_FAILURE, cMSG_CHANNEL_REQUEST,
            cMSG_GLOBAL_REQUEST, cMSG_CHANNEL_OPEN,
        )
    except ImportError:
        raise Failure("install paramiko==3.5.1 in an optional venv; see tests/settings-acceptance.md")
    require(paramiko.__version__ == "3.5.1", "private packet seam validated locally only with paramiko==3.5.1")
    deadline = time.monotonic() + a.max_total_seconds

    def expired(signum, frame):
        raise Failure("total probe deadline exceeded")

    # Hard cap as well as individual waits; intended for macOS/Linux operators.
    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, a.max_total_seconds)

    def bound():
        remaining = deadline - time.monotonic()
        require(remaining > 0, "total probe deadline exceeded")
        return min(a.timeout, remaining)

    class ProbeTransport(paramiko.Transport):
        """One outstanding request per transport; record actual SSH reply packets.

        Paramiko env/agent/resize APIs omit replies, and its normal channel-failure
        handler closes the channel locally. Neither is suitable denial evidence.
        Keep this private seam small and pinned; never mutate the class tables.
        """
        def __init__(self, *args, **kwargs):
            self.reply_event = threading.Event()
            self.reply = None
            self.pending = None
            super().__init__(*args, **kwargs)
            self._channel_handler_table = self._channel_handler_table.copy()
            self._channel_handler_table[MSG_CHANNEL_SUCCESS] = lambda ch, m: self.record(ch.chanid, True)
            self._channel_handler_table[MSG_CHANNEL_FAILURE] = lambda ch, m: self.record(ch.chanid, False)

        def record(self, target, accepted):
            if self.pending != target or self.reply_event.is_set():
                self.close()  # unsolicited/duplicate reply: cannot attribute it safely
                return
            self.reply = accepted
            self.reply_event.set()

        def _parse_request_success(self, m):
            self.record("global", True)

        def _parse_request_failure(self, m):
            self.record("global", False)

        def exchange(self, packet, target, label, expected):
            require(self.is_active(), f"{label}: transport already closed, not rejection evidence")
            self.reply = None
            self.reply_event.clear()
            self.pending = target
            self._send_user_message(packet)
            end = time.monotonic() + bound()
            while not self.reply_event.wait(min(0.05, max(0, end - time.monotonic()))):
                require(self.is_active(), f"{label}: disconnect without reply is inconclusive")
                require(time.monotonic() < end, f"{label}: reply timeout is inconclusive")
            self.pending = None
            require(self.reply is expected, f"{label}: got SSH {'SUCCESS' if self.reply else 'FAILURE'}, expected {'SUCCESS' if expected else 'FAILURE'}")
            passed(f"{label}: SSH {'SUCCESS' if expected else 'FAILURE'} reply")

        def request(self, ch, kind, fields=(), expected=False, label=None):
            require(not ch.closed, f"{kind}: channel already closed, not rejection evidence")
            m = Message()
            m.add_byte(cMSG_CHANNEL_REQUEST)
            m.add_int(ch.remote_chanid)
            m.add_string(kind)
            m.add_boolean(True)  # want_reply, including env, agent and window-change
            for field_type, value in fields:
                getattr(m, f"add_{field_type}")(value)
            self.exchange(m, ch.chanid, label or kind, expected)

        def resize_during_exit(self, ch, width, height):
            # Real terminals can resize while the app exits. No reply is required
            # for this lifecycle stress packet; it is NOT policy-denial evidence.
            m = Message()
            m.add_byte(cMSG_CHANNEL_REQUEST)
            m.add_int(ch.remote_chanid)
            m.add_string("window-change")
            m.add_boolean(False)
            for value in (width, height, 0, 0):
                m.add_int(value)
            self._send_user_message(m)

        def global_probe(self, kind, fields):
            m = Message()
            m.add_byte(cMSG_GLOBAL_REQUEST)
            m.add_string(kind)
            m.add_boolean(True)
            for field_type, value in fields:
                getattr(m, f"add_{field_type}")(value)
            self.exchange(m, "global", kind, False)

        def unix_channel(self, path):
            # open_channel() has no streamlocal payload support in Paramiko 3.5.1.
            with self.lock:
                chanid = self._next_channel()
                ch = paramiko.Channel(chanid)
                self._channels.put(chanid, ch)
                event = threading.Event()
                self.channel_events[chanid] = event
                self.channels_seen[chanid] = True
                ch._set_transport(self)
                ch._set_window(self.default_window_size, self.default_max_packet_size)
            m = Message()
            m.add_byte(cMSG_CHANNEL_OPEN)
            m.add_string("direct-streamlocal@openssh.com")
            m.add_int(chanid)
            m.add_int(self.default_window_size)
            m.add_int(self.default_max_packet_size)
            m.add_string(path)
            m.add_string("")  # reserved
            m.add_int(0)  # reserved
            self._send_user_message(m)
            require(event.wait(bound()), "direct Unix socket: channel-open reply timeout")
            result = self._channels.get(chanid)
            if result is not None:
                result.close()
                raise Failure("direct Unix socket: CHANNEL_OPEN_CONFIRMATION (accepted)")
            error = self.get_exception()
            require(isinstance(error, paramiko.ChannelException), "direct Unix socket: no explicit CHANNEL_OPEN_FAILURE")
            return error

    hosts = paramiko.HostKeys(str(a.known_hosts.expanduser()))
    host_name = a.host if a.port == 22 else f"[{a.host}]:{a.port}"
    require(hosts.lookup(host_name) is not None, f"no independently verified known host key for {host_name}")
    password = getpass.getpass("Private key passphrase (both keys): ") if a.ask_key_passphrase else None

    @contextlib.contextmanager
    def connection(username="settings", key=None):
        client = paramiko.SSHClient()
        client.load_host_keys(str(a.known_hosts.expanduser()))
        client.set_missing_host_key_policy(paramiko.RejectPolicy())
        try:
            client.connect(a.host, port=a.port, username=username,
                           key_filename=str((key or a.key).expanduser()), passphrase=password,
                           look_for_keys=False, allow_agent=False, timeout=bound(),
                           banner_timeout=bound(), auth_timeout=bound(), channel_timeout=bound(),
                           transport_factory=ProbeTransport)
            yield client.get_transport()
        finally:
            client.close()

    def session(t):
        ch = t.open_session(timeout=bound())
        ch.settimeout(bound())
        return ch

    def output(ch, predicate, label):
        data = bytearray()
        end = time.monotonic() + bound()
        while True:
            for ready, recv in ((ch.recv_ready, ch.recv), (ch.recv_stderr_ready, ch.recv_stderr)):
                while ready():
                    data.extend(recv(4096))
                    require(len(data) <= 262144, f"{label}: output exceeds 256 KiB")
            text = data.decode("utf-8", "replace")
            # Ignore terminal styling; never print raw, potentially hostile output.
            text = re.sub(r"\x1b\][^\x07]*(?:\x07|\x1b\\)", "", text)
            text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", text).replace("\r", "")
            if predicate(text):
                return text
            require(not ch.closed, f"{label}: closed before expected output")
            require(time.monotonic() < end, f"{label}: expected output/close not observed within bound")
            time.sleep(0.05)

    def closed(ch, label, success=True):
        output(ch, lambda _: ch.closed, label)
        require(ch.exit_status_ready(), f"{label}: missing exit status")
        status = ch.recv_exit_status()
        require(status == 0 if success else status > 0, f"{label}: unexpected exit status {status}")
        passed(f"{label}: channel closed, exit {status}")

    def pty(t, ch, width=80, height=24):
        t.request(ch, "pty-req", (("string", "xterm-256color"), ("int", width), ("int", height),
                                  ("int", 0), ("int", 0), ("string", b"\x00")), True)

    def screen(t, owner, width=80, height=24, spoof=False, other_owner=None):
        ch = session(t)
        pty(t, ch, width, height)
        if spoof:
            for name, value in (("DEV_BOX_AUTHENTICATED_OWNER", "spoofed-owner"),
                                ("SSH_ORIGINAL_COMMAND", "/bin/sh"), ("PATH", "/tmp"),
                                ("ENV", "/tmp/probe"), ("BASH_ENV", "/tmp/probe")):
                t.request(ch, "env", (("string", name), ("string", value)), label=f"pre-start env {name}")
        t.request(ch, "shell", expected=True)
        management_hint = r"d delete|· d ·"
        text = output(ch, lambda s: "dev.box settings" in s and owner in s and
                      re.search(management_hint, s) and re.search(r"\bq\b", s), "settings screen")
        owner_pattern = r"(?i:\bowner\b)[^\n\w]*" + re.escape(owner) + r"(?=\s|$)"
        require(re.search(owner_pattern, text), "screen lacks a labeled exact expected owner")
        if other_owner:
            require(not re.search(r"(?i:\bowner\b)[^\n\w]*" + re.escape(other_owner) + r"(?=\s|$)", text),
                    "screen leaked the other authenticated owner")
        require(re.search(management_hint, text), "screen lacks management-UI delete hint")
        passed("PTY screen: dev.box settings header, expected owner, delete hint and q")
        return ch

    def denied_open(t, kind, label, accepted_codes=(1, 3), **kwargs):
        try:
            ch = t.open_channel(kind, timeout=bound(), **kwargs)
        except paramiko.ChannelException as error:
            # CONNECT_FAILED (2) only proves destination failure, not policy.
            require(error.code in accepted_codes, f"{label}: failure code {error.code} is not the expected policy denial")
            passed(f"{label}: CHANNEL_OPEN_FAILURE code {error.code}")
        else:
            ch.close()
            raise Failure(f"{label}: CHANNEL_OPEN_CONFIRMATION (accepted)")

    def ordinary():
        with connection("ubuntu") as t:
            ch = session(t)
            t.request(ch, "exec", (("string", "printf '%s\\n' DEV_BOX_UBUNTU_CONTROL"),), True, "ubuntu exec")
            output(ch, lambda s: "DEV_BOX_UBUNTU_CONTROL" in s, "ubuntu positive control")
            closed(ch, "ubuntu positive control")

    def pods():
        cmd = ["kubectl", "--context", a.kube_context, "--namespace", a.session_namespace,
               "--request-timeout", f"{max(1, int(bound()))}s", "get", "pods", "-o", "json"]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=bound(), check=True)
        return json.loads(result.stdout)["items"]

    def ordinary_uid():
        matches = [p for p in pods() if p["metadata"]["name"] == a.ordinary_pod]
        require(len(matches) == 1, "existing ordinary pod is missing; do not recreate/delete it for this test")
        require(not matches[0]["metadata"].get("deletionTimestamp"), "ordinary pod is being deleted")
        return matches[0]["metadata"]["uid"]

    def settings_pods():
        # Let kubectl parse the supplied selector; no assumptions about app/chart labels.
        cmd = ["kubectl", "--context", a.kube_context, "--namespace", a.session_namespace,
               "--request-timeout", f"{max(1, int(bound()))}s", "get", "pods", "-l", a.settings_selector, "-o", "json"]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=bound(), check=True)
        return {p["metadata"]["uid"]: p for p in json.loads(result.stdout)["items"]}

    def observe_new(baseline):
        current = settings_pods()
        new = set(current) - set(baseline)
        require(len(new) == 1, "need exactly one fresh settings pod; selector incorrect or concurrent unrelated activity")
        uid = new.pop()
        p = current[uid]
        images = [(c["name"], c["image"]) for c in p["spec"]["containers"]]
        image_ids = [(c["name"], c.get("imageID", "unobserved")) for c in p.get("status", {}).get("containerStatuses", [])]
        print("OBSERVED: " + json.dumps({"pod": p["metadata"]["name"], "uid": uid,
                                        "images": images, "imageIDs": image_ids}), flush=True)
        return uid

    def cleanup(uid):
        end = time.monotonic() + bound()
        while uid in settings_pods():
            require(time.monotonic() < end, f"settings pod {uid}: cleanup not observed within bound")
            time.sleep(0.2)
        passed(f"full transport disconnect: settings pod UID {uid} absent")

    baseline = settings_pods() if a.kube_context else None
    old_uid = ordinary_uid() if a.kube_context else None
    if old_uid:
        print(f"OBSERVED: ordinary pod {a.ordinary_pod}, UID {old_uid}", flush=True)
    ordinary()  # Never infer settings denial from an unusable endpoint/key.
    try:
        previous = None
        for small in (False, True):
            with connection() as t:
                ch = screen(t, a.owner, 40 if small else 80, 12 if small else 24, spoof=not small)
                uid = observe_new(baseline) if a.kube_context else None
                if uid:
                    require(uid != previous, "settings reconnect reused pod UID")
                if not small:
                    denied_open(t, "session", "second session while TUI active", accepted_codes=(4,))
                    t.request(ch, "shell", label="repeated shell startup")
                    t.request(ch, "signal", (("string", "TERM"),), label="disabled signal delivery")
                    t.request(ch, "env", (("string", "DEV_BOX_AUTHENTICATED_OWNER"), ("string", "spoofed-owner")),
                              label="active env owner spoof")
                    t.request(ch, "window-change", (("int", 40), ("int", 12), ("int", 0), ("int", 0)), True)
                    end = time.monotonic() + min(1, bound())
                    output(ch, lambda _: time.monotonic() >= end, "resize stability")
                    require(not ch.closed and t.is_active(), "resize terminated the application")
                    # A static renderer need not emit unchanged lines on resize.
                    passed("resize: application stayed active; small-terminal rendering checked separately")
                ch.sendall(b"q")
                closed(ch, "q")
                denied_open(t, "session", "second session after q on same transport", accepted_codes=(4,))
                if uid:
                    remaining = settings_pods().get(uid)
                    require(remaining is not None and not remaining["metadata"].get("deletionTimestamp"),
                            "pod disappeared or began deletion before parent transport disconnect; inspect lifecycle")
                    passed("q closed session while parent transport and connection-mode pod remained")
            if uid:
                cleanup(uid)
                previous = uid

        if a.resize_exit_stress:
            for attempt in range(4):
                with connection() as t:
                    ch = screen(t, a.owner)
                    uid = observe_new(baseline) if a.kube_context else None
                    ch.sendall(b"q")
                    sent = 0
                    for index in range(32):
                        if ch.closed:
                            break
                        t.resize_during_exit(ch, 40 if index % 2 else 80, 12 if index % 2 else 24)
                        sent += 1
                        time.sleep(min(0.002, bound()))
                    require(sent > 0, "resize/exit stress: channel closed before any resize was sent; inconclusive")
                    closed(ch, f"resize/exit stress {attempt + 1}: q")
                    require(t.is_active(), "resize/exit stress: parent transport died")
                    denied_open(t, "session", "resize/exit stress: parent still replies", accepted_codes=(4,))
                    passed(f"resize/exit stress {attempt + 1}: {sent} resize packets, parent healthy")
                if uid:
                    cleanup(uid)
        else:
            print("NOT RUN: resize/exit stress (supply --resize-exit-stress after approval)", flush=True)

        for label, prefix in (("typed shell commands", b""), ("escape", b"\x1b"),
                              ("Ctrl-C", b"\x03"), ("Ctrl-Z suspend", b"\x1a")):
            with connection() as t:
                ch = screen(t, a.owner)
                # Split the marker so echoing typed input is not mistaken for execution.
                ch.sendall(prefix + b"\r/bin/sh\rprintf 'DEV_BOX_%s' ESCAPE_EXECUTED\r")
                end = time.monotonic() + min(2, bound())
                text = output(ch, lambda s: "DEV_BOX_ESCAPE_EXECUTED" in s or ch.closed or time.monotonic() >= end,
                              label)
                require("DEV_BOX_ESCAPE_EXECUTED" not in text, f"{label}: command executed")
                if ch.closed:
                    require(ch.exit_status_ready() and ch.recv_exit_status() >= 0, f"{label}: no application exit status")
                else:
                    require(t.is_active(), f"{label}: transport lost without application exit")
                    ch.sendall(b"q")
                    closed(ch, f"{label}: subsequent q")
                passed(f"{label}: no execution marker during bounded observation; application exited without shell evidence")

        with connection() as t:
            ch = session(t)
            t.request(ch, "shell", expected=True, label="non-PTY application startup")
            output(ch, lambda s: re.search(r"(?:PTY|terminal|ssh\s+-t)", s, re.I), "non-PTY helpful error")
            closed(ch, "non-PTY helpful failure", success=False)

        for use_pty in (False, True):
            for command in ("", "/bin/sh", "/usr/local/bin/dev-box-settings", "printf 'DEV_BOX_%s' ESCAPE_EXECUTED",
                            "scp -t /tmp/dev-box-probe", "scp -f /etc/passwd", "scp -O -t /tmp/dev-box-probe"):
                with connection() as t:
                    ch = session(t)
                    if use_pty:
                        pty(t, ch)
                    t.request(ch, "exec", (("string", command),), label=f"exec {command!r}, PTY={use_pty}")
        for subsystem in ("sftp", "arbitrary-probe-subsystem"):
            with connection() as t:
                t.request(session(t), "subsystem", (("string", subsystem),), label=f"subsystem {subsystem}")
        for kind, fields in (
            ("x11-req", (("boolean", False), ("string", "MIT-MAGIC-COOKIE-1"), ("string", "00" * 16), ("int", 0))),
            ("auth-agent-req@openssh.com", ()),
        ):
            with connection() as t:
                t.request(session(t), kind, fields)
        for label in ("direct TCP", "SOCKS underlying direct-tcpip"):
            with connection() as t:
                denied_open(t, "direct-tcpip", label, dest_addr=("127.0.0.1", a.port), src_addr=("127.0.0.1", 49152))
        with connection() as t:
            t.global_probe("tcpip-forward", (("string", "127.0.0.1"), ("int", 0)))
        path = f"/tmp/dev-box-settings-probe-{uuid.uuid4().hex}.sock"
        with connection() as t:
            error = t.unix_channel(path)
            require(error.code in (1, 3), f"direct Unix socket: failure code {error.code} is not policy denial")
            passed(f"direct Unix socket: CHANNEL_OPEN_FAILURE code {error.code}")
        with connection() as t:
            t.global_probe("streamlocal-forward@openssh.com", (("string", path),))
        if a.second_key:
            # Both separately authenticated connections are alive concurrently.
            with connection() as first, connection(key=a.second_key) as second:
                one = screen(first, a.owner, other_owner=a.second_owner)
                two = screen(second, a.second_owner, other_owner=a.owner)
                one.sendall(b"q")
                two.sendall(b"q")
                closed(one, "concurrent first owner q")
                closed(two, "concurrent second owner q")
            with connection(key=a.second_key) as t:
                ch = screen(t, a.second_owner, other_owner=a.owner)
                ch.sendall(b"q")
                closed(ch, "second owner reconnect q")
        else:
            print("NOT RUN: concurrent distinct-owner isolation (supply --second-key/--second-owner)")
    finally:
        ordinary()  # Reconnect even after a settings failure; never remove an ordinary box.
        if old_uid:
            require(ordinary_uid() == old_uid, "ordinary pod UID changed during acceptance")
            passed("ordinary ubuntu pod UID preserved across disconnect/reconnect")
    if baseline is not None:
        end = time.monotonic() + bound()
        while set(settings_pods()) - set(baseline):
            require(time.monotonic() < end, "settings pods remain after final disconnect")
            time.sleep(0.2)
        passed("no new settings pod UIDs remain after all probe transports disconnected")
    else:
        print("NOT RUN: pod UID/image/lifecycle observations (no kubectl options)")
    signal.setitimer(signal.ITIMER_REAL, 0)
    print("Completed selected SSH probes only; remaining issue #9 gates are in tests/settings-acceptance.md")


if __name__ == "__main__":
    try:
        main()
    except (Failure, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        sys.exit(1)
    except Exception as error:
        # Authentication/SSH protocol errors are failures, never security-denial passes.
        print(f"FAIL: {type(error).__name__}: {error}", file=sys.stderr)
        sys.exit(1)
