#!/usr/bin/env python3
"""Render and server-dry-run the finite admission extension on Fedora.

This program never applies resources. Registration requires the explicit shared
admission authorization handled by the task owner. Secrets stay in .private.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

OWNER = "finetuning-core-flow-20261014"
NAME = OWNER + "-admission"
SYSTEM = "ani-ft-core-system-20261014"
TENANT = "ani-ft-core-20261014"
TENANT_UID = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"
HOOK_IMAGE = "localhost/ani-cpu03@sha256:53f1200cc679ad827aaad9306310d5577e89c26e6876de6d61800655c07b5040"
SUPPORT_IMAGES = [
    "172.16.101.10:5000/ani-kubeflow/argoproj/argoexec:v3.7.3@sha256:427154a68a3b8ec22225bc46151db943a3d579d69baed2c0eec8c25920f1215a",
    "172.16.101.10:5000/ani-kubeflow/kubeflow/kfp-driver:2.16.0@sha256:249a0e2f2ce2ed078648c5bd3b90e61f202f4864d75376ba77853e81a53535cd",
    "172.16.101.10:5000/ani-kubeflow/kubeflow/kfp-launcher:2.16.0@sha256:6b2d6c5733d957c9f7831cafe12aebcf9ef8ec503f57b74ed2ab00d5db113d29",
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--component-image", required=True)
    parser.add_argument("--hook-image", default=HOOK_IMAGE)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--kubeconfig", required=True)
    args = parser.parse_args()
    if not Path("/etc/fedora-release").exists() or any(not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", image) for image in (args.component_image, args.hook_image)):
        raise SystemExit("Fedora and explicit immutable component image required")
    os.umask(0o077)
    base = [args.kubectl, "--kubeconfig", args.kubeconfig, "--request-timeout=30s"]
    def kubectl(arguments, body=None):
        result = subprocess.run(base + arguments, input=body, capture_output=True, timeout=45)
        if result.returncode:
            raise RuntimeError("private kubectl response suppressed: " + " ".join(arguments[:4]))
        return result.stdout
    cluster = json.loads(kubectl(["get", "namespace", "kube-system", "-o", "json"]))
    namespace = json.loads(kubectl(["get", "namespace", TENANT, "-o", "json"]))
    if cluster["metadata"]["uid"] != "5277649d-e28d-4a0a-ae76-8354365c63bc" or namespace["metadata"]["uid"] != TENANT_UID or namespace["metadata"]["labels"].get("ani.io/managed-by") != OWNER:
        raise RuntimeError("frozen task environment identity mismatch")
    private = args.root / ".private"
    private.mkdir(parents=True, exist_ok=True, mode=0o700)
    key, crt, csr = [private / ("admission." + suffix) for suffix in ("key", "crt", "csr")]
    extension = private / "admission-extensions.cnf"
    extension.write_text("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:" + NAME + "." + SYSTEM + ".svc,DNS:" + NAME + "." + SYSTEM + ".svc.cluster.local\n")
    if not crt.exists():
        commands = [
            ["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(csr), "-subj", "/CN=" + NAME],
            ["openssl", "x509", "-req", "-in", str(csr), "-CA", str(private / "task-ca.crt"), "-CAkey", str(private / "task-ca.key"), "-CAcreateserial", "-out", str(crt), "-days", "30", "-sha256", "-extfile", str(extension)],
        ]
        for command in commands:
            subprocess.run(command, check=True, capture_output=True, timeout=30)
    handler = Path(__file__).with_name("admission.py").read_text()
    def obj(api, kind, name=NAME, ns=SYSTEM, **fields):
        meta = {"name": name, "labels": {"ani.io/managed-by": OWNER}}
        if ns:
            meta["namespace"] = ns
        return {"apiVersion": api, "kind": kind, "metadata": meta, **fields}
    configuration = {"images": [*SUPPORT_IMAGES, args.component_image], "component_image": args.component_image}
    rows = [obj("v1", "ServiceAccount", automountServiceAccountToken=True),
            obj("v1", "ConfigMap", data={"admission.py": handler, "configuration.json": json.dumps(configuration, separators=(",", ":"))}),
            obj("v1", "Secret", type="kubernetes.io/tls", data={"tls.crt": base64.b64encode(crt.read_bytes()).decode(), "tls.key": base64.b64encode(key.read_bytes()).decode()}),
            obj("rbac.authorization.k8s.io/v1", "ClusterRole", ns=None, rules=[{"apiGroups": [""], "resources": ["namespaces"], "resourceNames": [TENANT], "verbs": ["get"]}]),
            obj("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", ns=None, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": NAME}, subjects=[{"kind": "ServiceAccount", "namespace": SYSTEM, "name": NAME}]),
            obj("rbac.authorization.k8s.io/v1", "Role", ns=TENANT, rules=[{"apiGroups": ["argoproj.io"], "resources": ["workflows"], "verbs": ["get"]}]),
            obj("rbac.authorization.k8s.io/v1", "RoleBinding", ns=TENANT, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": NAME}, subjects=[{"kind": "ServiceAccount", "namespace": SYSTEM, "name": NAME}])]
    container = {"name": "admission", "image": args.hook_image, "imagePullPolicy": "IfNotPresent", "command": ["/opt/venv/bin/python", "-I", "-B", "/app/admission.py"], "args": ["--config", "/app/configuration.json", "--certificate", "/tls/tls.crt", "--key", "/tls/tls.key"], "ports": [{"containerPort": 8443}], "volumeMounts": [{"name": "source", "mountPath": "/app", "readOnly": True}, {"name": "tls", "mountPath": "/tls", "readOnly": True}], "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}, "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}}, "readinessProbe": {"httpGet": {"scheme": "HTTPS", "path": "/healthz", "port": 8443}, "periodSeconds": 3}}
    rows += [obj("apps/v1", "Deployment", spec={"replicas": 1, "selector": {"matchLabels": {"app": NAME}}, "template": {"metadata": {"labels": {"app": NAME, "ani.io/managed-by": OWNER}, "annotations": {"ani.modeldev/admission-source-sha256": hashlib.sha256(handler.encode()).hexdigest()}}, "spec": {"serviceAccountName": NAME, "securityContext": {"fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}}, "containers": [container], "volumes": [{"name": "source", "configMap": {"name": NAME, "defaultMode": 292}}, {"name": "tls", "secret": {"secretName": NAME, "defaultMode": 288}}]}}}),
             obj("v1", "Service", spec={"selector": {"app": NAME}, "ports": [{"name": "https", "port": 443, "targetPort": 8443}]}),
             obj("networking.k8s.io/v1", "NetworkPolicy", spec={"podSelector": {"matchLabels": {"app": NAME}}, "policyTypes": ["Ingress"], "ingress": [{"from": [{"ipBlock": {"cidr": ip + "/32"}} for ip in ("172.16.101.10", "172.16.101.11", "172.16.101.12")], "ports": [{"protocol": "TCP", "port": 8443}]}]}),
             obj("admissionregistration.k8s.io/v1", "MutatingWebhookConfiguration", ns=None, webhooks=[{"name": NAME + ".ani.io", "admissionReviewVersions": ["v1"], "sideEffects": "None", "failurePolicy": "Fail", "matchPolicy": "Exact", "reinvocationPolicy": "Never", "timeoutSeconds": 10, "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": TENANT, "ani.io/managed-by": OWNER}}, "objectSelector": {"matchLabels": {"pipelines.kubeflow.org/v2_component": "true"}, "matchExpressions": [{"key": "ani.modeldev/step-role", "operator": "Exists"}, {"key": "workflows.argoproj.io/workflow", "operator": "Exists"}, {"key": "pipeline/runid", "operator": "Exists"}]}, "clientConfig": {"service": {"namespace": SYSTEM, "name": NAME, "path": "/mutate", "port": 443}, "caBundle": base64.b64encode((private / "task-ca.crt").read_bytes()).decode()}, "rules": [{"apiGroups": [""], "apiVersions": ["v1"], "operations": ["CREATE"], "resources": ["pods"], "scope": "Namespaced"}]}])]
    raw = json.dumps({"apiVersion": "v1", "kind": "List", "items": rows}, separators=(",", ":")).encode() + b"\n"
    path = private / "admission-manifest.json"
    path.write_bytes(raw)
    kubectl(["apply", "--dry-run=server", "-f", "-", "-o", "name"], raw)
    receipt = {"owner": OWNER, "namespace_uid": TENANT_UID, "status": "SERVER_DRY_RUN_ONLY", "registered": False, "manifest_reference": str(path), "manifest_sha256": hashlib.sha256(raw).hexdigest(), "handler_sha256": hashlib.sha256(handler.encode()).hexdigest(), "component_image": args.component_image, "hook_runtime_image": args.hook_image, "resources": [{"kind": r["kind"], "namespace": r["metadata"].get("namespace"), "name": r["metadata"]["name"]} for r in rows]}
    (args.root / "admission-dry-run.json").write_text(json.dumps(receipt, indent=2) + "\n")
    preview = json.loads(json.dumps(rows))
    for row in preview:
        if row["kind"] == "Secret":
            row["redacted_data_keys"] = sorted(row.pop("data"))
    (args.root / "admission-review.json").write_text(json.dumps({"review_only": True, "registered": False, "receipt": receipt, "resources": preview}, indent=2) + "\n")
    print(json.dumps({"phase": "TASK_ADMISSION_SERVER_DRY_RUN", "registered": False, "receipt": str(args.root / "admission-dry-run.json")}))


if __name__ == "__main__":
    main()
