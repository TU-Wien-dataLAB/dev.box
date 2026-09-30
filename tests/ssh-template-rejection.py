#!/usr/bin/env python3
"""Live SSH regression: a nonexistent template must deny promptly, not time out.

Usage: python3 tests/ssh-template-rejection.py --key ~/.ssh/slurm_tu_wien
Requires a local port-forward and a previously trusted host key. First connects
to a known template to verify the key works, then checks prompt rejection. The
positive control may create its persistent pod on first use.
"""
import argparse
import pathlib
import subprocess
import sys
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--key", required=True)
parser.add_argument("--host", default="localhost")
parser.add_argument("--port", default=2222, type=int)
parser.add_argument("--username", default="issue6-no-template")
parser.add_argument("--known-template", default="ubuntu")
parser.add_argument("--max-seconds", default=5, type=float)
args = parser.parse_args()
command = [
    "ssh", "-i", str(pathlib.Path(args.key).expanduser()),
    "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
    "-o", "StrictHostKeyChecking=yes", "-o", "PreferredAuthentications=publickey",
    "-p", str(args.port),
]
try:
    control = subprocess.run(command + [f"{args.known_template}@{args.host}", "true"],
                             capture_output=True, text=True, timeout=60)
except subprocess.TimeoutExpired:
    sys.exit("FAIL: known-template positive control timed out")
if control.returncode != 0:
    sys.exit(f"FAIL: known-template positive control failed: {control.stderr.strip()}")
start = time.monotonic()
try:
    result = subprocess.run(command + [f"{args.username}@{args.host}", "true"],
                            capture_output=True, text=True, timeout=args.max_seconds)
except subprocess.TimeoutExpired:
    sys.exit(f"FAIL: SSH did not reject unknown template within {args.max_seconds:g}s")
elapsed = time.monotonic() - start
if result.returncode != 255 or "Permission denied" not in result.stderr:
    sys.exit(f"FAIL: expected authentication denial, got exit {result.returncode}: {result.stderr.strip()}")
print(f"PASS: unknown template rejected in {elapsed:.2f}s with Permission denied (exit 255)")
