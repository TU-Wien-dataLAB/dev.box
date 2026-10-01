#!/usr/bin/env python3
"""Verify a public-key-only server without masking OpenSSH's method preferences.

Requires a trusted host key and an enrolled unencrypted (or agent-unlocked) key.
The positive control may create its persistent box. No pods are deleted.
"""
import argparse
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--key", required=True)
    parser.add_argument("--unenrolled-key", required=True)
    parser.add_argument("--host", default="localhost")
    parser.add_argument("--port", type=int, default=2222)
    parser.add_argument("--known-template", default="ubuntu")
    parser.add_argument("--unknown-template", default="issue8-no-template")
    parser.add_argument("--known-hosts", default="~/.ssh/known_hosts")
    parser.add_argument("--host-key-alias")
    parser.add_argument("--max-seconds", type=float, default=5)
    args = parser.parse_args()
    if args.max_seconds <= 0:
        parser.error("--max-seconds must be positive")
    if args.known_template == args.unknown_template:
        parser.error("positive and negative templates must differ")

    with tempfile.TemporaryDirectory(prefix="ssh-advertisement-") as directory:
        directory = pathlib.Path(directory)
        prompts = directory / "prompts"
        askpass = directory / "askpass"
        askpass.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$PROMPT_LOG"\nexit 1\n')
        askpass.chmod(0o700)
        env = dict(os.environ, LC_ALL="C", SSH_ASKPASS=str(askpass),
                   SSH_ASKPASS_REQUIRE="force", DISPLAY="advertisement-test", PROMPT_LOG=str(prompts))
        command = ["ssh", "-F", "/dev/null", "-vv", "-T",
                   "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
                   "-o", "UserKnownHostsFile=" + str(pathlib.Path(args.known_hosts).expanduser()),
                   "-o", "GlobalKnownHostsFile=/dev/null", "-p", str(args.port)]
        if args.host_key_alias:
            command += ["-o", "HostKeyAlias=" + args.host_key_alias]

        def connect(username, key, timeout):
            prompts.unlink(missing_ok=True)
            start = time.monotonic()
            try:
                result = subprocess.run(command + ["-i", str(pathlib.Path(key).expanduser()),
                                        f"{username}@{args.host}", "echo READY"],
                                        env=env, capture_output=True, text=True, timeout=timeout)
            except subprocess.TimeoutExpired:
                sys.exit(f"FAIL: {username} did not finish within {timeout:g}s")
            elapsed = time.monotonic() - start
            if prompts.exists():
                sys.exit(f"FAIL: unexpected prompt (use an unencrypted or unlocked key): {prompts.read_text().strip()}")
            methods = re.findall(r"Authentications that can continue: ([^\r\n]+)", result.stderr)
            if not methods or any(value != "publickey" for value in methods):
                sys.exit(f"FAIL: {username} advertised {methods!r}, expected only publickey\n{result.stderr}")
            if re.search(r"Next authentication method: (password|keyboard-interactive)", result.stderr):
                sys.exit(f"FAIL: {username} attempted a disabled method\n{result.stderr}")
            return result, elapsed

        control, _ = connect(args.known_template, args.key, 60)
        if control.returncode != 0 or control.stdout != "READY\n":
            sys.exit(f"FAIL: known-template positive control failed\n{control.stderr}")
        print("PASS: enrolled key connects to the known template")
        for label, username, key in [
            ("unknown template", args.unknown_template, args.key),
            ("unenrolled key", args.known_template, args.unenrolled_key),
        ]:
            result, elapsed = connect(username, key, args.max_seconds)
            if result.returncode != 255 or "Permission denied (publickey)" not in result.stderr:
                sys.exit(f"FAIL: {label}: expected public-key authentication denial\n{result.stderr}")
            print(f"PASS: {label} denied in {elapsed:.2f}s, publickey only, no prompts")
        reconnect, _ = connect(args.known_template, args.key, 60)
        if reconnect.returncode != 0 or reconnect.stdout != "READY\n":
            sys.exit(f"FAIL: known-template reconnect failed\n{reconnect.stderr}")
        print("PASS: enrolled key reconnects (verify the pod UID separately)")


if __name__ == "__main__":
    main()
