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
    username = "md-tenantenv-20261101"
    query = urllib.parse.urlencode({"noPaging": "true", "query": json.dumps(
        {"username": username, "tenant_id": 40}, separators=(",", ":"))})
    def matching_users():
        users = call("GET", "/api/v1/admin/users?" + query, token=token)["items"]
        require(all(u.get("username") == username and
                    u.get("tenantId", u.get("tenant_id")) == 40 for u in users),
                "USER_QUERY_NOT_EXACT")
        require(len(users) <= 1, "DUPLICATE_TEST_USER")
        return users
    users = matching_users()
    root = Path("/home/kubercloud/tenantenv-20261101/b-test-principal")
    if inp.get("create") and not users:
        root.mkdir(mode=0o700, exist_ok=True)
        require(not root.is_symlink() and root.stat().st_uid == os.getuid() and
                root.stat().st_mode & 0o777 == 0o700, "PRIVATE_JOURNAL_INVALID")
        issued = root / "create-issued.json"
        with issued.open("x") as f:
            os.chmod(issued, 0o600)
            json.dump({"username": username, "tenant_id": 40, "role_ids": [26],
                       "credential_values": "OMITTED", "observed_at":
                       datetime.datetime.now(datetime.timezone.utc).isoformat()}, f)
            f.flush()
            os.fsync(f.fileno())
        call("POST", "/api/v1/admin/users", {"data": {"tenantId": 40,
             "username": username, "realname": "ModelDev tenant environment acceptance",
             "status": "NORMAL", "roleIds": [26]}, "password": inp["password"],
             "activationMode": "IMMEDIATE"}, token)
        users = matching_users()
        require(len(users) == 1, "CREATED_USER_NOT_READ_BACK")
        (root / "create-observed.json").write_text(json.dumps({"id": users[0]["id"],
                                                               "username": username}))
        os.chmod(root / "create-observed.json", 0o600)
    result = {"schema": "ani.modeldev.b-test-principal.v1", "identity": target,
              "runtime": runtime, "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "status": "PREPARED" if users else "ABSENT_READ_ONLY",
              "user": {k: users[0][k] for k in ("id", "username", "tenantId", "roleIds", "status")
                       if k in users[0]} if users else None, "calls": calls,
              "new_role_or_grant": False, "password_or_token_recorded": False,
              "environment_permission_and_training_acceptance": "NOT_RUN"}
    if users and inp.get("create"):
        b_login = call("POST", "/api/v1/auth/password/login", {
            "tenant_name": tenant["code"], "username": username, "password": inp["password"]})
        require(bool(b_login.get("access_token")), "B_LOGIN_TOKEN_MISSING")
        result["b_authentication"] = "ACTUAL_HTTP200"
    return result


if __name__ == "__main__":
    try:
        value = main(json.load(sys.stdin))
    except Exception as error:
        value = {"status": "FAIL", "reason": str(error) if type(error) is RuntimeError else type(error).__name__,
                 "automatic_retry": False}
    print(json.dumps(value, indent=2))
    sys.exit(1 if value["status"] == "FAIL" else 0)
