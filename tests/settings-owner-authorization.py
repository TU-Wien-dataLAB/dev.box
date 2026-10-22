#!/usr/bin/env python3
"""Read-only, opt-in Kubernetes authorization acceptance for settings credentials.

Requires an explicit context and impersonation permission. Never requests a token,
executes in a pod, changes RBAC, or deletes a pod. See docs/operations.md.
"""
import argparse
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--service-account", required=True)
    parser.add_argument("--own-pod", required=True, action="append")
    parser.add_argument("--foreign-pod", required=True, action="append")
    parser.add_argument("--settings-pod", required=True)
    args = parser.parse_args()
    own = set(args.own_pod)
    forbidden = set(args.foreign_pod) | {args.settings_pod}
    if own & forbidden:
        parser.error("own and forbidden pod sets must be disjoint")
    prefix = [
        "kubectl", "--context", args.context, "--namespace", args.namespace,
        "--request-timeout=15s",
        "--as", f"system:serviceaccount:{args.namespace}:{args.service_account}",
        "--as-group", "system:serviceaccounts",
        "--as-group", f"system:serviceaccounts:{args.namespace}",
        "--as-group", "system:authenticated", "auth", "can-i",
    ]

    def check(verb, resource, allowed, subresource=None):
        command = prefix + [verb, resource]
        if subresource:
            command += ["--subresource", subresource]
        result = subprocess.run(command, capture_output=True, text=True, timeout=20)
        actual = result.stdout.strip()
        expected = "yes" if allowed else "no"
        if actual != expected or result.returncode != (0 if allowed else 1):
            raise SystemExit(
                f"FAIL {verb} {resource}: expected {expected}; "
                f"exit={result.returncode} stdout={actual!r} stderr={result.stderr.strip()!r}"
            )
        print(f"PASS {verb} {resource}: {expected}", flush=True)

    for name in sorted(own):
        for verb in ("get", "delete"):
            check(verb, f"pods/{name}", True)
        for verb in ("patch", "update"):
            check(verb, f"pods/{name}", False)
        check("create", f"pods/{name}", False, subresource="exec")
    for name in sorted(forbidden):
        for verb in ("get", "delete"):
            check(verb, f"pods/{name}", False)
    for verb in ("list", "watch", "create", "deletecollection"):
        check(verb, "pods", False)
    for resource in ("secrets", "serviceaccounts", "roles.rbac.authorization.k8s.io", "rolebindings.rbac.authorization.k8s.io"):
        for verb in ("get", "list", "create", "update", "delete"):
            check(verb, resource, False)


if __name__ == "__main__":
    main()
