#!/usr/bin/env python3
"""Freeze and optionally apply one task namespaced Trainer Runtime on Fedora."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re

OWNER = "finetuning-core-flow-20261014"
TENANT = "ani-ft-core-20261014"
NAMESPACE_UID = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--training-image", required=True)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not Path("/etc/fedora-release").exists() or not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", args.training_image):
        raise SystemExit("Fedora and final immutable training image required")
    os.umask(0o077)
    module_path = Path(__file__).resolve().parents[2] / "scripts/finetuning-core-flow/deploy.py"
    spec = importlib.util.spec_from_file_location("task_deploy", module_path)
    deploy = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(deploy)
    cluster = deploy.Cluster(args.kubectl, args.kubeconfig)
    cluster.guard()
    namespace = cluster.get("namespace", TENANT)
    if namespace["metadata"]["uid"] != NAMESPACE_UID or namespace["metadata"]["labels"].get("ani.io/managed-by") != OWNER:
        raise ValueError("task namespace identity mismatch")
    original = cluster.get("clustertrainingruntime", "ani-modeldev-cpu-uid10001-exit-v1-1eb08af8")
    frozen = original["spec"]
    job = frozen["template"]["spec"]["replicatedJobs"][0]
    if job["name"] != "node" or len(frozen["template"]["spec"]["replicatedJobs"]) != 1:
        raise ValueError("source Runtime structure drift")
    job_spec = job["template"]["spec"]
    job_spec["activeDeadlineSeconds"] = 3600
    pod = job_spec["template"]["spec"]
    if pod["automountServiceAccountToken"] is not False or pod["restartPolicy"] != "Never" or pod["serviceAccountName"] != "trainer-workload" or pod["securityContext"]["fsGroup"] != 10001 or len(pod["containers"]) != 1:
        raise ValueError("source Runtime identity contract drift")
    node = pod["containers"][0]
    node["image"] = args.training_image
    node["command"] = ["/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"]
    node["resources"] = {"requests": {"cpu": "2", "memory": "6Gi", "ephemeral-storage": "10Gi"}, "limits": {"cpu": "4", "memory": "8Gi", "ephemeral-storage": "12Gi"}}
    node["securityContext"]["readOnlyRootFilesystem"] = True
    # Go json.Marshal(map) sorts keys. No floating fields or escaped strings
    # occur in this finite Runtime; this matches trainer.safeRuntime's digest.
    digest = hashlib.sha256(json.dumps(frozen, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
    name = "ani-ft-core-cpu-3600-" + digest[:12]
    value = {"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainingRuntime", "metadata": {"namespace": TENANT, "name": name, "labels": {"ani.io/managed-by": OWNER}}, "spec": frozen}
    cluster.run(["apply", "--dry-run=server", "-f", "-", "-o", "name"], deploy.encoded(value))
    if args.apply:
        cluster.apply(value)
        actual = cluster.get("trainingruntime", name, TENANT)
        actual_digest = hashlib.sha256(json.dumps(actual["spec"], sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
        if actual_digest != digest:
            raise ValueError("admitted Runtime spec differs; freeze actual defaults before any Release use")
    else:
        actual = None
    manifest = args.root / "training-runtime.json"
    deploy.save(manifest, value)
    ref = {"name": name, "kind": "TrainingRuntime", "api_group": "trainer.kubeflow.org", "content_sha256": digest, "target_jobs": ["node"]}
    deploy.save(args.root / "training-runtime-receipt.json", {"runtime": ref, "namespace_uid": NAMESPACE_UID, "runtime_uid": actual["metadata"]["uid"] if actual else None, "phase": "APPLIED_READBACK_CONFIRMED" if actual else "SERVER_DRY_RUN_ONLY", "source_runtime_uid": original["metadata"]["uid"], "training_image": args.training_image, "manifest_reference": str(manifest), "commands": cluster.receipts})
    print(json.dumps({"phase": "TASK_RUNTIME_APPLIED" if actual else "TASK_RUNTIME_DRY_RUN", "runtime": ref, "receipt": str(args.root / "training-runtime-receipt.json")}))


if __name__ == "__main__":
    main()
