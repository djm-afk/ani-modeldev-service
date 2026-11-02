#!/usr/bin/env python3
"""One ordinary B test-user creation; credentials/tokens stay in memory.

Read the root-maintained maintenance helper in memory from stdin, then verify
the same complete cluster identity and maintenance runtime before any write.
An issued marker forbids an automatic replay after an unknown response.
"""
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.parse
import urllib.request
import urllib.error


def main(inp):
    env = {"__name__": "maintenance_helpers"}
    exec(compile(inp["maintenance_helper"], "reviewed-maintenance-helper", "exec"), env)
    require, kget = env["require"], env["kget"]
    target = env["identity"]()
    runtime = env["current"](inp["expected_image"])
    require(kget("-n", "ani-system", "get", "svc", "ani-governance")["metadata"]["uid"] ==
            "2d4310e8-e5ed-4aef-b8d1-2af22dbd83c8", "GOV_SERVICE_CHANGED")
    ns = kget("get", "ns", "tenant-207328fc-f08d-4dc4-b403-16ec86396ce2")
    require(ns["metadata"]["uid"] == "8698b0b5-f4c8-4770-85e5-10f25ad7b347" and
            not ns["metadata"].get("deletionTimestamp"), "B_NAMESPACE_CHANGED")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), env["NoRedirect"]())
    calls = []

    def call(method, path, body=None, token=None):
        data = None if body is None else json.dumps(body).encode()
        headers = {"Accept": "application/json", "Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(env["BASE"] + path, data, headers, method=method)
        try:
            response = opener.open(req, timeout=25)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(262145)
            require(len(raw) <= 262144, "RESPONSE_TOO_LARGE")
            value = json.loads(raw)
            calls.append({"method": method, "path": path.split("?")[0],
                          "status": response.status,
                          "response_sha256": hashlib.sha256(raw).hexdigest()})
            require(response.status in (200, 201), "HTTP_" + str(response.status) + "_" +
                    method + "_" + path.split("?")[0])
            return value

    login = call("POST", "/api/v1/auth/platform/password/login",
                 {"username": "root", "password": inp["password"]})
    token = login["access_token"]
    tenant = call("GET", "/api/v1/admin/tenants/40", token=token)
    require(tenant.get("id") == 40 and tenant.get("code") == "e2e-full-20261029",
            "B_TENANT_CHANGED")
    # Resource UUID is an internal owner field, absent from the Tenant DTO.
    query_result = subprocess.run(["kubectl", "--context", env["CONTEXT"], "-n", "ani-platform",
        "exec", "-i", "postgresql-0", "--", "sh", "-c",
        'PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" '
        'PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=10000" '
        'exec psql -XqAt -U postgres -d ani_governance -v ON_ERROR_STOP=1'],
        input=b"SELECT json_build_object('read_only',current_setting('transaction_read_only'),"
              b"'id',id,'code',code,'resource_tenant_id',resource_tenant_id,'status',status) "
              b"FROM sys_tenants WHERE id=40 AND deleted_at IS NULL;",
        capture_output=True, timeout=25)
    require(query_result.returncode == 0, "B_OWNER_READ_FAILED")
    owner = json.loads(query_result.stdout)
    require(owner == {"read_only": "on", "id": 40, "code": "e2e-full-20261029",
                     "resource_tenant_id": "207328fc-f08d-4dc4-b403-16ec86396ce2", "status": "ON"},
            "B_RESOURCE_OWNER_CHANGED")
    role = call("GET", "/api/v1/admin/roles/26", token=token)
    require(role.get("code") == "tenant:manager" and role.get("type") == "TENANT" and
            role.get("tenantId", role.get("tenant_id")) == 40 and role.get("status") == "ON",
            "B_ORIGINAL_ROLE_CHANGED")
    # One task writer; all changes use the existing platform CRUD endpoints.
    root = Path("/home/kubercloud/tenantenv-20261101/b-private-plan")
    name = "modeldev-live-b-20261101"
    def listing(resource, filt):
        path = "/api/v1/admin/" + resource + "?" + urllib.parse.urlencode({
            "noPaging": "true", "query": json.dumps(filt, separators=(",", ":"))})
        return call("GET", path, token=token).get("items", [])
    def children(pid):
        modules = listing("plan-modules", {"plan_id": pid})
        quotas = listing("plan-quotas", {"plan_id": pid})
        require(all(x.get("planId") == pid for x in modules + quotas), "PLAN_CHILD_QUERY_NOT_EXACT")
        module_set = sorted(x["module"] for x in modules)
        quota_map = {x["quotaCode"]: str(x["quotaValue"]) for x in quotas}
        require(len(set(module_set)) == len(module_set) and len(quota_map) == len(quotas),
                "DUPLICATE_PLAN_CHILDREN")
        return module_set, quota_map
    source = call("GET", "/api/v1/admin/plans/2", token=token)
    require(source.get("id") == 2 and source.get("name") == "ani-factor10-e2e", "SOURCE_PLAN_CHANGED")
    modules, quotas = children(2)
    require(modules and "gpu.shared_memory_mib" not in quotas and
            quotas.get("gpu.physical.count") == "1", "SOURCE_POLICIES_CHANGED")
    plans = listing("plans", {"name": name})
    require(not plans, "TASK_PLAN_ALREADY_PRESENT_RECONCILE_ONLY")
    require(tenant.get("planId") == 2, "B_PLAN_POINTER_CHANGED")
    data = {k: source[k] for k in ("version", "expiryPolicy", "dataRetentionDays", "description", "remark") if k in source}
    data["name"] = name
    result = {"schema": "ani.modeldev.b-private-plan.v1", "identity": target, "runtime": runtime,
              "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "status": "READ_ONLY_PLAN", "source_plan": source, "source_modules": modules,
              "source_quotas": quotas, "new_plan_data": data,
              "only_added_quota": {"gpu.shared_memory_mib": "49140"}, "calls": calls}
    if not inp.get("apply_plan"):
        return result
    root.mkdir(mode=0o700, exist_ok=True)
    require(not root.is_symlink() and root.stat().st_uid == os.getuid() and
            root.stat().st_mode & 0o777 == 0o700, "PRIVATE_JOURNAL_INVALID")
    def mutate(label, method, path, body):
        issued = root / (label + "-issued.json")
        with issued.open("x") as f:
            os.chmod(issued, 0o600)
            json.dump({"method": method, "path": path, "body": body}, f)
            f.flush(); os.fsync(f.fileno())
        response = call(method, path, body, token)
        observed = root / (label + "-observed.json")
        with observed.open("x") as f:
            os.chmod(observed, 0o600);json.dump(response, f)
    mutate("plan", "POST", "/api/v1/admin/plans", {"data": data})
    plans = listing("plans", {"name": name})
    require(len(plans) == 1 and plans[0].get("name") == name, "CREATED_PLAN_NOT_UNIQUE")
    pid = plans[0]["id"]
    require(isinstance(pid, int) and pid > 3, "INVALID_NEW_PLAN_ID")
    require(not listing("tenants", {"plan_id": pid}), "NEW_PLAN_ALREADY_REFERENCED")
    for n, module in enumerate(modules):
        mutate("module-" + str(n), "POST", "/api/v1/admin/plan-modules", {"data": {"planId": pid, "module": module}})
    for n, (code, value) in enumerate(sorted(quotas.items())):
        mutate("quota-" + str(n), "POST", "/api/v1/admin/plan-quotas", {"data": {"planId": pid, "quotaCode": code, "quotaValue": value}})
    mutate("shared-quota", "POST", "/api/v1/admin/plan-quotas", {"data": {"planId": pid, "quotaCode": "gpu.shared_memory_mib", "quotaValue": "49140"}})
    expected = dict(quotas, **{"gpu.shared_memory_mib": "49140"})
    require(children(pid) == (modules, expected), "NEW_PLAN_COPY_MISMATCH")
    require(children(2) == (modules, quotas) and call("GET", "/api/v1/admin/plans/2", token=token) == source,
            "ORIGINAL_PLAN_CHANGED")
    require(call("GET", "/api/v1/admin/tenants/40", token=token) == tenant,
            "B_CHANGED_BEFORE_ASSIGNMENT")
    mutate("bind-b-only", "PUT", "/api/v1/admin/tenants/40", {"id": 40,
           "data": {"planId": pid}, "updateMask": "planId", "allowMissing": False})
    final = call("GET", "/api/v1/admin/tenants/40", token=token)
    require(final.get("planId") == pid and final.get("id") == 40 and final.get("code") == tenant["code"], "B_ASSIGNMENT_READBACK_MISMATCH")
    references = listing("tenants", {"plan_id": pid})
    require(len(references) == 1 and references[0].get("id") == 40, "NEW_PLAN_SCOPE_NOT_B_ONLY")
    require(children(2) == (modules, quotas), "ORIGINAL_PLAN_CHILDREN_CHANGED")
    result.update(status="B_PRIVATE_PLAN_BOUND", private_plan_id=pid, private_modules=modules,
                  private_quotas=expected, tenant_references=[40], preserved_shared_plan_id=2,
                  quota_accounts_or_charges_written=False, native_cas=False,
                  concurrency="Single task writer; complete source and immediate B state rechecked before assignment; ordinary API has no conditional update field",
                  gpu_profile_selectability_and_training="NOT_RUN")
    return result


if __name__ == "__main__":
    try:
        value = main(json.load(sys.stdin))
    except Exception as error:
        value = {"status": "FAIL", "reason": str(error) if type(error) is RuntimeError else type(error).__name__,
                 "automatic_retry": False}
    print(json.dumps(value, indent=2))
    sys.exit(1 if value["status"] == "FAIL" else 0)
