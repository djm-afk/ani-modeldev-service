#!/usr/bin/env python3
"""Native, dry-run-only TrainJob admission probes using real TokenRequest.

No Runtime/TrainJob is created. A missing real Runtime remains a separate
provider precondition, never a reason to create a substitute live Runtime.
"""
import argparse
import base64
import copy
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import ssl
import subprocess
import urllib.error
import urllib.request

from render import NAME, PREFIX


def probes(config):
    execution = "6f370a93-cde4-44a9-a137-240f98e53ac8"
    now = datetime.now(timezone.utc)
    annotations = {PREFIX + key: "11111111-2222-4333-8444-555555555555" for key in ["create-operation-id", "workspace-uid", "runtime-uid"]}
    annotations.update({PREFIX + "tenant-id": "46936e6b-3a3b-4868-8d88-6833665abb70", PREFIX + "execution-id": execution})
    annotations.update({PREFIX + key: "a" * 64 for key in ["execution-spec-sha256", "training-request-sha256", "runtime-spec-sha256"]})
    annotations.update({PREFIX + "training-admitted-at": now.isoformat(), PREFIX + "training-deadline-at": (now + timedelta(minutes=15)).isoformat()})
    base = {"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob", "metadata": {"name": "md-" + execution, "namespace": config["namespace"], "annotations": annotations}, "spec": {"runtimeRef": {"name": "mdrt-" + "1" * 48, "kind": "TrainingRuntime", "apiGroup": "trainer.kubeflow.org"}, "managedBy": "trainer.kubeflow.org/trainjob-controller", "suspend": False, "trainer": {"image": config["cpu_image"], "numNodes": 1, "numProcPerNode": 1, "command": ["/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"], "args": ["--data", "/inputs/data.csv", "--output", "/outputs"], "resourcesPerNode": {"requests": {"cpu": "1", "memory": "1Gi"}, "limits": {"cpu": "1", "memory": "1Gi"}}}}}
    cases = [("cpu-baseline", True, base)]
    large = copy.deepcopy(base)
    large["spec"]["trainer"]["resourcesPerNode"] = {group: {"cpu": "4", "memory": "8Gi"} for group in ["requests", "limits"]}
    cases.append(("cpu-nonlegacy-capacity", True, large))
    for mode, memory, cores in [("shared", "8192", "50"), ("whole", "15360", "100")]:
        gpu = copy.deepcopy(large)
        gpu["spec"]["trainer"]["image"] = config["gpu_image"]
        for values in gpu["spec"]["trainer"]["resourcesPerNode"].values():
            values.update({"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": memory, "volcano.sh/vgpu-cores": cores})
        cases.append(("gpu-" + mode, True, gpu))
    mutations = {
        "cluster-runtime": lambda obj: obj["spec"]["runtimeRef"].update(kind="ClusterTrainingRuntime"),
        "foreign-runtime-name": lambda obj: obj["spec"]["runtimeRef"].update(name="foreign-runtime"),
        "unknown-image": lambda obj: obj["spec"]["trainer"].update(image="example.invalid/arbitrary:latest"),
        "two-nodes": lambda obj: obj["spec"]["trainer"].update(numNodes=2),
        "automatic-processes": lambda obj: obj["spec"]["trainer"].update(numProcPerNode="auto"),
        "missing-hash": lambda obj: obj["metadata"]["annotations"].pop(PREFIX + "training-request-sha256"),
        "bad-uuid": lambda obj: obj["metadata"]["annotations"].update({PREFIX + "runtime-uid": "not-an-original-uid"}),
        "bad-hash": lambda obj: obj["metadata"]["annotations"].update({PREFIX + "runtime-spec-sha256": "wrong"}),
        "tenant-mismatch": lambda obj: obj["metadata"]["annotations"].update({PREFIX + "tenant-id": "11111111-2222-4333-8444-555555555555"}),
        "execution-name": lambda obj: obj["metadata"].update(name="md-11111111-2222-4333-8444-555555555555"),
        "secret-initializer": lambda obj: obj["spec"].update(initializer={"model": {"storageUri": "s3://probe/model", "env": [{"name": "TOKEN", "valueFrom": {"secretKeyRef": {"name": "dry-run-only", "key": "token"}}}]}}),
        "secret-env": lambda obj: obj["spec"]["trainer"].update(env=[{"name": "TOKEN", "valueFrom": {"secretKeyRef": {"name": "dry-run-only", "key": "token"}}}]),
        "claims": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"].update(claims=[{"name": "dry-run-only"}]),
        "control-label": lambda obj: obj["spec"].update(labels={"pipelines.kubeflow.org/v2_component": "true"}),
        "pod-overrides": lambda obj: obj["spec"].update(podTemplateOverrides=[{"targetJobs": [{"name": "node"}], "spec": {"serviceAccountName": "modeldev-control"}}]),
        "metadata-control-label": lambda obj: obj["metadata"].update(labels={"pipelines.kubeflow.org/v2_component": "true"}),
        "zero-cpu": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"]["requests"].update(cpu="0"),
        "unmatched-limits": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"]["limits"].update(cpu="2"),
        "missing-memory": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"]["requests"].pop("memory"),
        "unsupported-gpu-key": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"]["requests"].update({"nvidia.com/gpu": "1"}),
        "partial-gpu-vector": lambda obj: obj["spec"]["trainer"]["resourcesPerNode"]["requests"].update({"volcano.sh/vgpu-number": "1"}),
    }
    for name, mutate in mutations.items():
        value = copy.deepcopy(base)
        mutate(value)
        cases.append((name, False, value))
    return cases


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--kubectl", type=Path, required=True)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--phase", choices=["before-legacy-exclusion", "active"], default="active")
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    k = [str(args.kubectl), "--kubeconfig", str(args.kubeconfig), "--request-timeout=15s"]
    original = json.loads(subprocess.check_output(k + ["config", "view", "--minify", "--raw", "-o", "json"]))
    cluster = original["clusters"][0]["cluster"]
    context = ssl.create_default_context(cadata=base64.b64decode(cluster["certificate-authority-data"]).decode())
    token = subprocess.check_output(k + ["-n", "ani-system", "create", "token", "modeldev-control", "--duration=10m"], text=True).strip()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    url = cluster["server"].rstrip("/") + "/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + config["namespace"] + "/trainjobs?dryRun=All&fieldValidation=Strict"
    results = []
    for name, valid, value in probes(config):
        if args.phase == "before-legacy-exclusion" and not valid and name not in ["cluster-runtime", "two-nodes", "secret-env", "unmatched-limits"]:
            continue
        req = urllib.request.Request(url, data=json.dumps(value).encode(), method="POST", headers={"Authorization": "Bearer " + token, "Content-Type": "application/json", "Accept": "application/json"})
        try:
            with opener.open(req, timeout=20) as response:
                status, body = response.status, json.loads(response.read(1024 * 1024))
        except urllib.error.HTTPError as error:
            status, body = error.code, json.loads(error.read(1024 * 1024))
        message = body.get("message", "")
        category = "ACCEPTED_DRY_RUN" if status == 201 else "V2_POLICY_DENIED" if NAME in message else "LEGACY_POLICY_DENIED" if "ani-kfp-trainjob" in message else "OTHER_NATIVE_PRECONDITION"
        results.append({"case": name, "positive": valid, "http_status": status, "category": category, "message": message[:1500], "live_resource_created": False})
    receipt = {"observed_at": datetime.now(timezone.utc).isoformat(), "phase": args.phase, "caller": "system:serviceaccount:ani-system:modeldev-control", "authentication": "real TokenRequest for the original Kubernetes API audience, original CA; no impersonation", "only_native_dry_run": True, "results": results, "all_positive_admitted": all(item["http_status"] == 201 for item in results if item["positive"]), "all_negative_denied": all(item["http_status"] >= 400 for item in results if not item["positive"])}
    args.output.write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({key: value for key, value in receipt.items() if key != "results"} | {"cases": [{key: value for key, value in item.items() if key not in ["message"]} for item in results]}, indent=2))


if __name__ == "__main__":
    main()
