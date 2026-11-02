#!/usr/bin/env python3
"""Run only through the designated SSH entry after the original Workbench job succeeds.

Input JSON arrives on stdin. The password and bearer token remain in memory.
This checks maintenance denial, not training acceptance or Stop/release behavior.
"""
import base64
import datetime
import getpass
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
import uuid

CONTEXT = "kubernetes-admin@ani-kcn-full"
DEPLOYMENT_UID = "4ae87828-5f2c-4b27-a8b8-d348aeb86ee5"
OLD_POD_UID = "76211ecd-65c5-43f2-9c17-f373df78ce20"
CLUSTER_ID = "f39bf6e2-f3ed-4191-ada3-caeebf86c220"
KUBE_SYSTEM_UID = "3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24"
BASE = "http://10.96.44.233:7788"
FLAG = "ANI_MODELDEV_CREATE_PAUSED"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def require(value, code):
    if not value:
        raise RuntimeError(code)


def kget(*args):
    r = subprocess.run(["kubectl", "--context", CONTEXT,
                        "--request-timeout=10s", *args, "-o", "json"],
                       capture_output=True, timeout=20)
    require(r.returncode == 0, "KUBERNETES_READ_FAILED")
    return json.loads(r.stdout)


def identity():
    import yaml
    require(getpass.getuser() == "kubercloud" and os.environ.get("SSH_CONNECTION"),
            "DESIGNATED_SSH_REQUIRED")
    cfg = kget("config", "view", "--minify")
    require(cfg.get("current-context") == CONTEXT and
            cfg["clusters"][0]["cluster"]["server"] == "https://lb.kubesphere.local:6443",
            "CONTEXT_OR_APISERVER_MISMATCH")
    require(kget("get", "ns", "kube-system")["metadata"]["uid"] == KUBE_SYSTEM_UID,
            "KUBE_SYSTEM_UID_MISMATCH")
    secret = kget("-n", "ani-system", "get", "secret", "ani-resource-private")
    private = yaml.safe_load(base64.b64decode(secret["data"]["config.yaml"], validate=True))
    require(private["network"]["cluster_id"] == CLUSTER_ID, "CLUSTER_ID_MISMATCH")
    return {"ssh": "kubercloud@100.64.128.12", "context": CONTEXT,
            "apiserver": cfg["clusters"][0]["cluster"]["server"],
            "cluster_id": CLUSTER_ID, "kube_system_uid": KUBE_SYSTEM_UID}


def current(image):
    d = kget("-n", "ani-system", "get", "deployment", "ani-governance")
    require(d["metadata"]["uid"] == DEPLOYMENT_UID, "DEPLOYMENT_UID_MISMATCH")
    c = next(c for c in d["spec"]["template"]["spec"]["containers"] if c["name"] == "governance")
    require(c["image"] == image, "DEPLOYMENT_IMAGE_MISMATCH")
    # The maintenance candidate defaults to paused. Accept only the already
    # identified source, and inspect only the precise maintenance key.
    sources = c.get("envFrom", [])
    require(len(sources) == 1 and sources[0].get("prefix", "") == "" and
            sources[0].get("secretRef", {}).get("name") == "ani-governance-environment",
            "PAUSE_ENVFROM_CHANGED")
    s = kget("-n", "ani-system", "get", "secret", "ani-governance-environment")
    effective = None
    if FLAG in s.get("data", {}):
        effective = base64.b64decode(s["data"][FLAG], validate=True).decode()
    for e in c.get("env", []):
        if e.get("name") == FLAG:
            require("valueFrom" not in e, "PAUSE_REFERENCE_CHANGED")
            effective = e.get("value", "")
    require(effective in (None, "", "true"), "CREATE_NOT_CONFIGURED_PAUSED")
    labels = d["spec"]["selector"].get("matchLabels", {})
    require(labels and not d["spec"]["selector"].get("matchExpressions"), "SELECTOR_CHANGED")
    pods = kget("-n", "ani-system", "get", "pods", "-l",
                ",".join(k + "=" + v for k, v in sorted(labels.items())))["items"]
    require(len(pods) == 1 and pods[0]["metadata"]["uid"] != OLD_POD_UID and
            not pods[0]["metadata"].get("deletionTimestamp"), "OLD_OR_EXTRA_POD_PRESENT")
    pod = pods[0]
    owners = pod["metadata"].get("ownerReferences", [])
    require(len(owners) == 1 and owners[0].get("controller") is True and
            owners[0]["kind"] == "ReplicaSet", "POD_OWNER_MISMATCH")
    rs = kget("-n", "ani-system", "get", "replicaset", owners[0]["name"])
    require(rs["metadata"]["uid"] == owners[0]["uid"] and
            any(o.get("controller") is True and o.get("kind") == "Deployment" and
                o.get("uid") == DEPLOYMENT_UID for o in rs["metadata"].get("ownerReferences", [])),
            "REPLICASET_OWNER_MISMATCH")
    cs = next(v for v in pod["status"]["containerStatuses"] if v["name"] == "governance")
    require(cs["ready"] and cs.get("imageID", "").endswith("@" + image.split("@", 1)[1]),
            "READY_IMAGE_ID_MISMATCH")
    return {"deployment_uid": DEPLOYMENT_UID, "generation": d["metadata"]["generation"],
            "pod_uid": pod["metadata"]["uid"], "pod_name": pod["metadata"]["name"],
            "image": image, "image_id": cs["imageID"], "pause_value": effective or "default"}


def ledger():
    query = """BEGIN READ ONLY;
SELECT json_build_object(
 'read_only',current_setting('transaction_read_only'),
 'acceptance_count',(SELECT count(*) FROM sys_modeldev_acceptances),
 'create_fence',(SELECT coalesce(json_agg(json_build_array(id,operation_id,execution_id,
 dispatch_state,attempt_count,lease_generation,lease_owner,lease_until,retry_blocked)
 ORDER BY id),'[]') FROM sys_modeldev_acceptances),
 'active_create_leases',(SELECT count(*) FROM sys_modeldev_acceptances WHERE lease_until>clock_timestamp()),
 'operation_count',(SELECT count(*) FROM sys_quota_operations WHERE owner_service='ani-modeldev-service'),
 'charges',(SELECT coalesce(json_agg(json_build_array(c.charge_id,c.original_units,c.released_units)
 ORDER BY c.charge_id),'[]') FROM sys_quota_charges c JOIN sys_quota_operations o
 ON o.operation_id=c.operation_id AND o.tenant_id=c.tenant_id WHERE o.owner_service='ani-modeldev-service'));
COMMIT;"""
    command = ["kubectl", "--context", CONTEXT, "--request-timeout=10s", "-n", "ani-platform",
               "exec", "-i", "postgresql-0", "--", "sh", "-c",
               'PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" '
               'PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=10000" '
               'exec psql -XqAt -U postgres -d ani_governance -v ON_ERROR_STOP=1']
    r = subprocess.run(command, input=query.encode(), capture_output=True, timeout=35)
    require(r.returncode == 0, "READ_ONLY_LEDGER_FAILED")
    data = json.loads(r.stdout)
    require(data["read_only"] == "on", "READ_ONLY_LEDGER_NOT_CONFIRMED")
    for key in ("create_fence", "charges"):
        data[key + "_sha256"] = hashlib.sha256(json.dumps(data.pop(key),
            separators=(",", ":"), sort_keys=True).encode()).hexdigest()
    return data


def run(inp):
    image = inp["expected_image"]
    require(re.fullmatch(r"10\.10\.1\.66:30003/ani-gpu-platform/ani-governance@sha256:[0-9a-f]{64}", image),
            "EXPECTED_IMAGE_INVALID")
    target = identity()
    before = current(image)
    service = kget("-n", "ani-system", "get", "service", "ani-governance")
    require(service["metadata"]["uid"] == "2d4310e8-e5ed-4aef-b8d1-2af22dbd83c8" and
            service["spec"]["clusterIP"] == "10.96.44.233", "SERVICE_IDENTITY_CHANGED")
    old = ledger()
    require(old["active_create_leases"] == 0, "CREATE_LEASES_NOT_DRAINED")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    response_sha = {}
    def post(path, body, token=None):
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(BASE + path, json.dumps(body).encode(), headers, method="POST")
        try:
            response = opener.open(req, timeout=25)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(65537)
            require(len(raw) <= 65536, "HTTP_RESPONSE_TOO_LARGE")
            response_sha[path] = hashlib.sha256(raw).hexdigest()
            return response.status, json.loads(raw)
    status, login = post("/api/v1/auth/password/login", {
        "tenant_name": "tenant-a", "username": "admin", "password": inp["password"]})
    require(status == 200 and isinstance(login.get("access_token"), str) and login["access_token"],
            "TENANT_AUTHENTICATION_FAILED")
    # The caller reserves this ID before transport, so an unknown response can
    # be reconciled against the same exact request without creating a new one.
    probe_id = inp["probe_id"]
    require(str(uuid.UUID(probe_id)) == probe_id, "PROBE_ID_INVALID")
    compute_id = str(uuid.uuid5(uuid.UUID(probe_id), "nonexistent-compute-denial-probe"))
    dataset_id = str(uuid.uuid5(uuid.UUID(probe_id), "nonexistent-dataset-denial-probe"))
    status, denial = post("/api/v1/modeldev/executions", {
        "name": "maintenance-denial-" + probe_id, "kind": "GENERAL_TRAINING",
        "implementation_id": "builtin.mlp", "compute_preset_id": compute_id,
        "dataset_version_id": dataset_id, "parameters": {}, "idempotency_key": probe_id},
        login["access_token"])
    require(status == 503 and denial.get("reason") == "MODELDEV_CREATE_MAINTENANCE",
            "MAINTENANCE_DENIAL_NOT_PROVED")
    new = ledger()
    after = current(image)
    require(old == new and before == after, "ADMISSION_OR_LEDGER_CHANGED")
    return {"schema": "ani.modeldev.governance-maintenance-acceptance.v1",
            "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "status": "PASS", "identity": target, "runtime": after,
            "tenant_authentication": "actual login HTTP200; token omitted",
            "probe_id": probe_id, "create_http_status": status,
            "create_reason": denial["reason"],
            "create_response_sha256": response_sha["/api/v1/modeldev/executions"],
            "ledger_before": old, "ledger_after": new,
            "scope": "Create denial, drained leases and no new acceptance/charge/publisher claims only",
            "stop_close_release": "NOT_RUN by this probe", "root_apply_authorized": False}


if __name__ == "__main__":
    try:
        raw = sys.stdin.buffer.read(65537)
        require(len(raw) <= 65536, "INPUT_TOO_LARGE")
        result = run(json.loads(raw))
    except Exception as error:
        # Never forward arbitrary HTTP, SQL, login or infrastructure error text.
        reason = str(error) if type(error) is RuntimeError else type(error).__name__
        result = {"status": "FAIL", "reason": reason, "actual_gate_passed": False}
    print(json.dumps(result, indent=2))
    sys.exit(0 if result["status"] == "PASS" else 1)
