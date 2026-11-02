#!/usr/bin/env python3
"""Server dry-run probes with an existing real principal and its original groups.

This tool never requests a token, impersonates, or persists an object. Probe
manifests and expected decisions are explicit audited input, not a local CEL
substitute. A generic RBAC denial cannot pass an admission-denial assertion.
"""
import argparse
import json
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--expected-username", required=True)
    parser.add_argument("--expected-group", action="append", default=[])
    parser.add_argument("--cases", type=Path, required=True)
    args = parser.parse_args()
    if args.context != "kubernetes-admin@ani-kcn-full":
        raise SystemExit("designated cluster context required")
    command = ["kubectl", "--kubeconfig", str(args.kubeconfig), "--context", args.context, "--request-timeout=8s"]
    identity = subprocess.run(command + ["auth", "whoami", "-o", "json"], capture_output=True, timeout=10)
    if identity.returncode:
        raise SystemExit("real principal identity unavailable; NOT_RUN")
    try:
        user = json.loads(identity.stdout)["status"]["userInfo"]
        if user["username"] != args.expected_username or set(user.get("groups", [])) != set(args.expected_group):
            raise ValueError()
    except (ValueError, KeyError):
        raise SystemExit("principal or original groups mismatch; NOT_RUN")
    document = json.loads(args.cases.read_bytes())
    if set(document) != {"cases"} or not 1 <= len(document["cases"]) <= 64:
        raise SystemExit("bounded explicit cases required")
    results = []
    for case in document["cases"]:
        if set(case) != {"id", "allowed", "operation", "object"} or case["operation"] not in {"CREATE", "UPDATE"} or type(case["allowed"]) is not bool:
            raise SystemExit("exact dry-run case shape required")
        operation = "create" if case["operation"] == "CREATE" else "replace"
        response = subprocess.run(command + [operation, "--dry-run=server", "-f", "-", "-o", "name"], input=json.dumps(case["object"]).encode(), capture_output=True, timeout=12)
        actual = response.returncode == 0
        modeldev_denial = b"ModelDev" in response.stderr or b"modeldev-" in response.stderr or b"modeldev.ani.io" in response.stderr
        result = "PASS" if actual == case["allowed"] and (actual or modeldev_denial) else "FAIL" if actual else "BLOCKED"
        results.append({"id": case["id"], "expected_allowed": case["allowed"], "actual_allowed": actual, "modeldev_admission_denial": modeldev_denial, "result": result})
    print(json.dumps({"username": user["username"], "groups": user.get("groups", []), "dry_run_only": True, "results": results}, separators=(",", ":")))
    raise SystemExit(0 if all(r["result"] == "PASS" for r in results) else 1)


if __name__ == "__main__":
    main()
