"""Use the normal authorized Stop API once for four fixed original commands."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.request

IDS = ["27cf73a3-4107-4c07-86f7-862ce9fb413d", "c9b470e0-d9cb-4989-a7f2-d1fbfbe72ab9",
       "91e351e7-102e-4807-b0d2-9236c1426da9", "f148135c-640f-47ed-b9b1-4b039d4709d1"]


def main():
    inp = json.load(sys.stdin)
    support = {"__name__": "maintenance_support"}
    exec(inp["support_source"], support)
    identity = support["identity"]()
    runtime = support["current"](inp["expected_image"])
    before = support["ledger"]()
    assert before["active_create_leases"] == 0
    previous = Path("/home/kubercloud/tenantenv-20261101/four-original-http-stop/27cf73a3-4107-4c07-86f7-862ce9fb413d-response.json")
    rejected = json.loads(previous.read_text())
    assert rejected["http_status"] == 400 and rejected["response"]["reason"] == "INVALID_MODELDEV_STOP"
    # The custom native HTTP handler rejects every non-empty body before the
    # service. The first {} request was definitely rejected; use its actual
    # empty-body contract and retain the first response instead of deleting it.
    root = Path("/home/kubercloud/tenantenv-20261101/four-original-http-stop-empty-body")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    assert not root.is_symlink() and root.stat().st_uid == os.getuid() and root.stat().st_mode & 0o077 == 0
    assert all(f.name[:-12] in IDS for f in root.glob("*-issued.json")), "UNKNOWN_ISSUE_MARKER"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), support["NoRedirect"]())

    def call(path, body, token=None):
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(support["BASE"] + path, b"" if body is None else json.dumps(body).encode(), headers, method="POST")
        try:
            response = opener.open(req, timeout=30)
        except urllib.error.HTTPError as e:
            response = e
        with response:
            raw = response.read(65537)
            assert len(raw) <= 65536
            return response.status, json.loads(raw), hashlib.sha256(raw).hexdigest()

    status, login, _ = call("/api/v1/auth/password/login", {
        "tenant_name": "tenant-a", "username": "admin", "password": inp["password"]})
    assert status == 200 and login.get("access_token"), "ACTUAL_TENANT_LOGIN_FAILED"
    results = []
    for execution in IDS:
        marker = root / (execution + "-issued.json")
        if marker.exists():
            response = root / (execution + "-response.json")
            assert response.exists(), "UNKNOWN_EXISTING_POST_READ_INTENTS_NO_RETRY"
            saved = json.loads(response.read_text())
            assert saved["http_status"] in (200, 202) and saved["response"].get("execution_id") == execution and saved["response"].get("stop_requested") is True, "EXISTING_POST_NOT_CONFIRMED"
            results.append({**saved, "observed_prior_accepted_response_no_repost": True})
            continue
        with marker.open("x") as out:
            os.chmod(out.name, 0o600)
            json.dump({"execution_id": execution, "method": "POST", "path":
                "/api/v1/modeldev/executions/" + execution + ":stop",
                "issued_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat()}, out)
            out.flush(); os.fsync(out.fileno())
        status, body, sha = call("/api/v1/modeldev/executions/" + execution + ":stop", None, login["access_token"])
        value = {"execution_id": execution, "http_status": status, "response_sha256": sha,
                 "response": {k: body[k] for k in ("operation_id", "execution_id", "stop_requested",
                               "intent_generation", "replayed", "reason") if k in body}}
        (root / (execution + "-response.json")).write_text(json.dumps(value, indent=2) + "\n")
        results.append(value)
        assert status in (200, 202) and body.get("execution_id") == execution and body.get("stop_requested") is True, "NORMAL_STOP_NOT_CONFIRMED_READ_INTENTS_NO_RETRY"
    after = support["ledger"]()
    assert before == after and runtime == support["current"](inp["expected_image"]), "CREATE_OR_ACCOUNTING_CHANGED"
    result = {"schema": "ani.modeldev.four-original-http-stop.v1", "observed_at_utc":
        datetime.datetime.now(datetime.timezone.utc).isoformat(), "status": "PASS", "identity": identity,
        "runtime": runtime, "login_http": 200, "actor_authorization": "Normal Stop endpoint authorizes actual tenant-a/admin user",
        "results": results, "create_and_charge_ledger_before": before, "create_and_charge_ledger_after": after,
        "scope": "Normal durable Stop intent only; owner fence ACK and CLOSED must be read separately; no Create sent"}
    (root / "complete.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(json.dumps({"status": "UNRESOLVED_READ_FIXED_NODE_JOURNALS_AND_ORIGINAL_INTENTS",
            "reason": str(e) if type(e) is AssertionError else type(e).__name__, "automatic_retry": False}))
        raise SystemExit(1)
