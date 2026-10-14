#!/usr/bin/env python3
"""Read-only Fedora preflight for the authorized .10-.12 CORE_FLOW cluster.

The caller supplies an existing, private kubeconfig. This program never creates
resources, issues tokens, reads Secret values, or writes credentials to its report.
Component readiness is environment evidence, not business acceptance.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess


EXPECTED_IPS = {"172.16.101.10", "172.16.101.11", "172.16.101.12"}


def identity(obj):
    meta = obj["metadata"]
    return {"name": meta["name"], "uid": meta["uid"],
            "namespace": meta.get("namespace"),
            "managed_by": meta.get("labels", {}).get("ani.io/managed-by")}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--expected-cluster-uid", required=True)
    parser.add_argument("--task-namespace", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    report = {"schema": "ani.finetuning.environment-preflight.v1",
              "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "scope": "CORE_FLOW", "business_acceptance": "NOT_RUN",
              "host": socket.gethostname(), "commands": [], "blockers": []}
    base = [args.kubectl, "--kubeconfig", args.kubeconfig,
            "--request-timeout=30s"]

    def command(parts, required=True):
        try:
            result = subprocess.run(base + parts, capture_output=True, timeout=45)
        except subprocess.TimeoutExpired:
            report["commands"].append({"args": parts, "exit_code": 124})
            if required:
                report["blockers"].append({"command": parts,
                                           "reason": "kubectl exceeded 45-second bound"})
            return None
        report["commands"].append({"args": parts, "exit_code": result.returncode})
        if result.returncode:
            # Command output may contain private data; retain only the bounded
            # command and exit code. kubectl describes the source on replay.
            if required:
                report["blockers"].append({"command": parts,
                                           "reason": "kubectl command failed"})
            return None
        return result.stdout

    def get(kind, name=None, namespace=None, required=True):
        parts = ["get", kind] + ([name] if name else [])
        if namespace:
            parts += ["-n", namespace]
        raw = command(parts + ["-o", "json"], required)
        return json.loads(raw) if raw is not None else None

    def finish(code):
        report["status"] = "BLOCKED" if report["blockers"] else "OBSERVED"
        Path(args.output).write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({"status": report["status"], "output": args.output,
                          "cluster_uid": report.get("cluster", {}).get("uid"),
                          "blocker_count": len(report["blockers"])}))
        return code

    current = command(["config", "current-context"])
    server = command(["config", "view", "--minify", "-o",
                      "jsonpath={.clusters[0].cluster.server}"])
    cluster = get("namespace", "kube-system")
    if cluster is None:
        return finish(1)
    nodes = get("nodes")
    if nodes is None:
        return finish(1)
    actual_ips = {a["address"] for n in nodes["items"]
                  for a in n["status"]["addresses"] if a["type"] == "InternalIP"}
    report["cluster"] = {"context": current.decode().strip() if current else None,
                         "api_server": server.decode() if server else None,
                         "uid": cluster["metadata"]["uid"], "nodes": []}
    for n in nodes["items"]:
        row = identity(n)
        row.update({"internal_ips": [a["address"] for a in n["status"]["addresses"]
                                      if a["type"] == "InternalIP"],
                    "kubelet_version": n["status"]["nodeInfo"]["kubeletVersion"],
                    "allocatable": n["status"].get("allocatable", {}),
                    "ready": next((c["status"] for c in n["status"]["conditions"]
                                   if c["type"] == "Ready"), None)})
        report["cluster"]["nodes"].append(row)
    if cluster["metadata"]["uid"] != args.expected_cluster_uid or actual_ips != EXPECTED_IPS:
        report["blockers"].append({"reason": "authorized cluster identity mismatch",
                                   "recovery": "supply the verified .10-.12 cluster kubeconfig and UID"})
        return finish(1)

    classes = get("storageclass")
    report["storage_classes"] = [{**identity(c), "provisioner": c["provisioner"],
                                   "reclaim_policy": c.get("reclaimPolicy"),
                                   "binding_mode": c.get("volumeBindingMode")}
                                  for c in (classes or {}).get("items", [])]
    runtimes = get("clustertrainingruntime")
    report["training_runtimes"] = []
    for runtime in (runtimes or {}).get("items", []):
        jobs = runtime["spec"].get("template", {}).get("spec", {}).get("replicatedJobs", [])
        row = {**identity(runtime), "spec_sha256": hashlib.sha256(
            json.dumps(runtime["spec"], sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
            "jobs": []}
        for job in jobs:
            pod = job.get("template", {}).get("spec", {}).get("template", {}).get("spec", {})
            row["jobs"].append({"name": job["name"], "pod_security_context": pod.get("securityContext"),
                                "service_account": pod.get("serviceAccountName"),
                                "containers": [{"name": c["name"], "image": c["image"],
                                                "security_context": c.get("securityContext"),
                                                "resources": c.get("resources")}
                                               for c in pod.get("containers", [])]})
        report["training_runtimes"].append(row)

    namespaces = get("namespaces")
    names = {n["metadata"]["name"]: n for n in (namespaces or {}).get("items", [])}
    selected = ["kubeflow", "kubeflow-system", "ani-platform", "ani-system",
                "ani-kfp-manual-a", "ani-kfp-manual-b", args.task_namespace]
    report["namespaces"] = []
    for name in selected:
        if name not in names:
            report["namespaces"].append({"name": name, "exists": False})
            continue
        row = {**identity(names[name]), "exists": True}
        deployments = get("deployments", namespace=name)
        row["deployments"] = [{**identity(d), "ready_replicas": d.get("status", {}).get("readyReplicas", 0),
                                "desired_replicas": d["spec"].get("replicas", 1),
                                "images": [c["image"] for c in d["spec"]["template"]["spec"]["containers"]]}
                               for d in (deployments or {}).get("items", [])]
        services = get("services", namespace=name)
        row["services"] = [{**identity(s), "cluster_ip": s["spec"].get("clusterIP"),
                             "type": s["spec"].get("type"), "ports": s["spec"].get("ports", [])}
                            for s in (services or {}).get("items", [])]
        accounts = get("serviceaccounts", namespace=name)
        row["service_accounts"] = [{**identity(s), "automount_token": s.get("automountServiceAccountToken")}
                                    for s in (accounts or {}).get("items", [])]
        # JSONPath limits Secret inspection to metadata and key names; values
        # never enter this process or the saved report.
        secret_refs = command(["get", "secrets", "-n", name, "-o",
                               "go-template={{range .items}}{{.metadata.name}}:{{range $k,$v := .data}}{{$k}},{{end}}{{\"\\n\"}}{{end}}"])
        row["secret_key_references"] = secret_refs.decode().splitlines() if secret_refs else []
        policies = get("networkpolicies,resourcequotas,limitranges,pvc", namespace=name)
        row["isolation_objects"] = [{**identity(p), "kind": p["kind"],
                                     "status": p.get("status") if p["kind"] == "ResourceQuota" else None}
                                    for p in (policies or {}).get("items", [])]
        report["namespaces"].append(row)
    report["unverified"] = ["task-owned database and service identity",
                            "task namespace isolation and UID", "actual UID10001 workspace write",
                            "KFP authenticated business objects", "task-scoped S3 IO",
                            "LoRA and CORE_FLOW business acceptance"]
    return finish(1 if report["blockers"] else 0)


if __name__ == "__main__":
    raise SystemExit(main())
