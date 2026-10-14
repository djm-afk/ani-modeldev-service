"""Finite task CREATE mutation, based on the deployed exit-retention handler.

The preserved finalizer and AdmissionReview/TLS mechanism come from
ani-system/ani-modeldev-exit-retention-config. This handler additionally checks
the live namespace and Workflow identities before applying the task contract.
It never handles UPDATE, another namespace or unlabeled platform Pods.
"""
import argparse
import base64
import copy
import json
import os
import re
import ssl
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import Request, urlopen

FINALIZER = "modeldev.ani.io/step-exit-evidence"
NAMESPACE = "ani-ft-core-20261014"
NAMESPACE_UID = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"
OWNER = "finetuning-core-flow-20261014"
CONTROLLER = "system:serviceaccount:kubeflow:argo"
MAX_BODY = 1048576
ROLE_MOUNTS = {
    "workspace-name": {}, "train-wait": {}, "register": {}, "close": {}, "finalize-close": {},
    "prepare": {"/workspace": ("", False)},
    "export": {"/workspace/inputs": ("inputs", True), "/workspace/training": ("training", True),
               "/workspace/reports/train": ("reports/train", True), "/workspace/export": ("export", False),
               "/workspace/reports/export": ("reports/export", False)},
    "verify": {"/workspace/inputs": ("inputs", True), "/workspace/export": ("export", True),
               "/workspace/reports/export": ("reports/export", True), "/workspace/reports/verify": ("reports/verify", False)},
    "collect": {"/workspace": ("", True), "/workspace/publication": ("publication", False)},
    "publish": {"/workspace": ("", True), "/workspace/publication": ("publication", False)},
}


def canonical_uuid(value):
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value and uuid.UUID(value).int != 0
    except (ValueError, AttributeError):
        return False


class KubernetesReader:
    """Two bounded GETs with the task ServiceAccount; no general discovery."""
    def __init__(self):
        self.directory = Path("/var/run/secrets/kubernetes.io/serviceaccount")
        self.context = ssl.create_default_context(cafile=str(self.directory / "ca.crt"))
        self.base = "https://" + os.environ["KUBERNETES_SERVICE_HOST"] + ":" + os.environ["KUBERNETES_SERVICE_PORT_HTTPS"]

    def get(self, path):
        token = (self.directory / "token").read_text().strip()
        request = Request(self.base + path, headers={"Authorization": "Bearer " + token, "Accept": "application/json"})
        with urlopen(request, context=self.context, timeout=2) as response:
            raw = response.read(MAX_BODY + 1)
        if len(raw) > MAX_BODY:
            raise ValueError("bounded identity response required")
        return json.loads(raw)

    def identities(self, workflow):
        return (self.get("/api/v1/namespaces/" + NAMESPACE),
                self.get("/apis/argoproj.io/v1alpha1/namespaces/" + NAMESPACE + "/workflows/" + workflow))


def review(body, configuration, identities):
    if body.get("apiVersion") != "admission.k8s.io/v1" or body.get("kind") != "AdmissionReview":
        raise ValueError("AdmissionReview v1 required")
    request = body.get("request")
    if not isinstance(request, dict) or not canonical_uuid(request.get("uid")):
        raise ValueError("request UID required")
    response = {"uid": request["uid"], "allowed": True}
    result = {"apiVersion": "admission.k8s.io/v1", "kind": "AdmissionReview", "response": response}
    if request.get("namespace") != NAMESPACE or request.get("operation") != "CREATE" or request.get("resource") != {"group": "", "version": "v1", "resource": "pods"} or request.get("subResource", ""):
        return result

    def refuse():
        response["allowed"] = False
        response["status"] = {"code": 403, "message": "Frozen task namespace, Workflow, role, image and mount association required"}
        return result

    pod = request.get("object")
    if not isinstance(pod, dict):
        return refuse()
    metadata, spec = pod.get("metadata", {}), pod.get("spec", {})
    labels = metadata.get("labels", {})
    if labels.get("pipelines.kubeflow.org/v2_component") != "true":
        return result
    role = labels.get("ani.modeldev/step-role")
    owners = metadata.get("ownerReferences", [])
    if role not in ROLE_MOUNTS or request.get("userInfo", {}).get("username") != CONTROLLER or pod.get("apiVersion") != "v1" or pod.get("kind") != "Pod" or metadata.get("namespace") != NAMESPACE or spec.get("serviceAccountName") != "modeldev-step" or len(owners) != 1:
        return refuse()
    owner = owners[0]
    if owner.get("apiVersion") != "argoproj.io/v1alpha1" or owner.get("kind") != "Workflow" or owner.get("controller") is not True or not re.fullmatch(r"general-cpu-[a-z0-9]+", owner.get("name", "")) or not canonical_uuid(owner.get("uid")) or labels.get("workflows.argoproj.io/workflow") != owner["name"] or not canonical_uuid(labels.get("pipeline/runid")):
        return refuse()
    namespace, workflow = identities(owner["name"])
    if namespace.get("metadata", {}).get("uid") != NAMESPACE_UID or namespace["metadata"].get("labels", {}).get("ani.io/managed-by") != OWNER or workflow.get("metadata", {}).get("uid") != owner["uid"] or workflow["metadata"].get("namespace") != NAMESPACE or workflow["metadata"].get("labels", {}).get("pipeline/runid") != labels["pipeline/runid"]:
        return refuse()
    containers = spec.get("containers", [])
    if not isinstance(containers, list) or {c.get("name") for c in containers} != {"main", "wait"} or spec.get("ephemeralContainers") or spec.get("hostNetwork") or spec.get("hostPID") or spec.get("hostIPC"):
        return refuse()
    for category in ("containers", "initContainers"):
        declared = spec.get(category, [])
        if not isinstance(declared, list) or any(not isinstance(c, dict) or c.get("image") not in configuration["images"] for c in declared):
            return refuse()
    main = next(c for c in containers if c["name"] == "main")
    if main["image"] != configuration["component_image"]:
        return refuse()
    volumes = spec.get("volumes", [])
    if any("hostPath" in v for v in volumes):
        return refuse()
    pvc_names = {v["name"] for v in volumes if "persistentVolumeClaim" in v}
    if len(pvc_names) != (1 if ROLE_MOUNTS[role] else 0) or any(not re.fullmatch(r"ani-kfp-workspace-[a-z0-9-]+", v["persistentVolumeClaim"].get("claimName", "")) for v in volumes if "persistentVolumeClaim" in v):
        return refuse()
    mounts = [m for m in main.get("volumeMounts", []) if m.get("name") in pvc_names]
    if len(mounts) != len(ROLE_MOUNTS[role]) or {m.get("mountPath") for m in mounts} != set(ROLE_MOUNTS[role]) or any(m.get("subPath", "") != ROLE_MOUNTS[role][m["mountPath"]][0] or m.get("subPathExpr") for m in mounts):
        return refuse()
    finalizers = metadata.get("finalizers", [])
    if not isinstance(finalizers, list) or any(not isinstance(f, str) for f in finalizers) or len(finalizers) != len(set(finalizers)):
        return refuse()
    changed = copy.deepcopy(spec)
    security = changed.setdefault("securityContext", {})
    security.update({"fsGroup": 10001, "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": {"type": "RuntimeDefault"}})
    for category in ("containers", "initContainers"):
        for container in changed.get(category, []):
            cs = container.setdefault("securityContext", {})
            cs.update({"readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False, "runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "capabilities": {"drop": ["ALL"]}})
            for mount in container.get("volumeMounts", []):
                if mount.get("name") in pvc_names:
                    mount["readOnly"] = ROLE_MOUNTS[role][mount["mountPath"]][1] if container["name"] == "main" else True
                elif mount.get("mountPath") in {"/etc/modeldev-step", "/kfp/certs"}:
                    mount["readOnly"] = True
            if container["name"] == "main":
                cs.update({"runAsUser": 10001, "runAsGroup": 10001})
                resources = container.setdefault("resources", {})
                resources.setdefault("requests", {})["ephemeral-storage"] = "10Gi"
                resources.setdefault("limits", {})["ephemeral-storage"] = "12Gi"
    patch = [{"op": "replace", "path": "/spec", "value": changed},
             {"op": "add", "path": "/metadata/finalizers", "value": finalizers if FINALIZER in finalizers else [*finalizers, FINALIZER]}]
    response["patchType"] = "JSONPatch"
    response["patch"] = base64.b64encode(json.dumps(patch, separators=(",", ":")).encode()).decode()
    return result


def server(address, certificate, key, configuration, identities):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        def log_message(self, *_):
            pass
        def send(self, status, raw):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(raw)
        def do_GET(self):
            self.send(200 if self.path == "/healthz" else 404, b"{}")
        def do_POST(self):
            if self.path != "/mutate" or self.headers.get("Content-Type", "").split(";", 1)[0] != "application/json":
                self.send(400, b"{}")
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= MAX_BODY or self.headers.get("Transfer-Encoding"):
                    raise ValueError("bounded body required")
                self.connection.settimeout(1.5)
                raw = self.rfile.read(length)
                if len(raw) != length:
                    raise ValueError("complete body required")
                self.send(200, json.dumps(review(json.loads(raw), configuration, identities), separators=(",", ":")).encode())
            except Exception:
                # FailurePolicy=Fail; no private token or upstream exception in logs.
                self.send(400, b"{}")
    listener = ThreadingHTTPServer(address, Handler)
    listener.daemon_threads = True
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.load_cert_chain(certificate, key)
    listener.socket = context.wrap_socket(listener.socket, server_side=True)
    return listener


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--certificate", required=True)
    parser.add_argument("--key", required=True)
    args = parser.parse_args()
    configuration = json.loads(Path(args.config).read_bytes())
    if set(configuration) != {"images", "component_image"} or configuration["component_image"] not in configuration["images"] or any(not re.search(r"@sha256:[0-9a-f]{64}$", x) for x in configuration["images"]):
        raise SystemExit("immutable finite image configuration required")
    server(("0.0.0.0", 8443), args.certificate, args.key, configuration, KubernetesReader().identities).serve_forever()
