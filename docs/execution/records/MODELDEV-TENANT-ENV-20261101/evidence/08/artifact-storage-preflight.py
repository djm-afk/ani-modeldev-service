#!/usr/bin/env python3
"""Read-only dedicated artifact storage preflight, through the designated SSH node.

Credentials stay in memory. TLS keeps the original DNS authority and SNI while
the socket uses the observed Kubernetes Service VIP. No bucket/IAM/Kube writes.
"""
import base64
import datetime
import getpass
import hashlib
import hmac
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import sys
import urllib.parse
import xml.etree.ElementTree as ET
import yaml

CTX = "kubernetes-admin@ani-kcn-full"
HOST = "ani-rustfs-svc.ani-platform.svc.cluster.local"
BUCKET = "ani-md-tenantenv-kfp-20261101"
SECRET = "modeldev-tenantenv-artifact-source-20261101"
CA_SHA = "e8ce7d3b810977805834b968604c3610342a1366be420d9729edc44bc1d96236"


def require(ok, code):
    if not ok:
        raise RuntimeError(code)


def get(*args):
    p = subprocess.run(["kubectl", "--context", CTX, "--request-timeout=10s",
                        *args, "-o", "json"], capture_output=True, timeout=20)
    require(p.returncode == 0, "KUBE_READ_FAILED")
    return json.loads(p.stdout) if p.stdout.strip() else None


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def main():
    require(getpass.getuser() == "kubercloud" and os.environ.get("SSH_CONNECTION"),
            "DESIGNATED_SSH_REQUIRED")
    cfg = get("config", "view", "--minify")
    require(cfg["current-context"] == CTX and cfg["clusters"][0]["cluster"]["server"]
            == "https://lb.kubesphere.local:6443", "CONTEXT_MISMATCH")
    require(get("get", "ns", "kube-system")["metadata"]["uid"] ==
            "3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24", "CLUSTER_UID_MISMATCH")
    rp = get("-n", "ani-system", "get", "secret", "ani-resource-private")
    private = yaml.safe_load(base64.b64decode(rp["data"]["config.yaml"], validate=True))
    require(private["network"]["cluster_id"] ==
            "f39bf6e2-f3ed-4191-ada3-caeebf86c220", "CLUSTER_ID_MISMATCH")
    service = get("-n", "ani-platform", "get", "service", "ani-rustfs-svc")
    require(service["spec"]["clusterIP"] not in ("", "None") and
            any(p["port"] == 9000 for p in service["spec"]["ports"]), "SERVICE_MISMATCH")
    dep = get("-n", "ani-platform", "get", "deployment", "ani-rustfs")
    require(dep["metadata"]["uid"] == "644c24df-70df-4cc0-a6e2-60e8846a647c",
            "STORAGE_DEPLOYMENT_UID_MISMATCH")
    require(any(e.get("secretRef", {}).get("name") == "ani-rustfs-root"
                for c in dep["spec"]["template"]["spec"]["containers"] for e in c.get("envFrom", [])),
            "STORAGE_ROOT_REFERENCE_MISMATCH")
    root = get("-n", "ani-platform", "get", "secret", "ani-rustfs-root")
    require(root["metadata"]["uid"] == "184259bb-2ed0-46ad-8301-f00f828f9d72",
            "STORAGE_ROOT_UID_MISMATCH")
    access = base64.b64decode(root["data"]["RUSTFS_ACCESS_KEY"], validate=True).decode()
    secret = base64.b64decode(root["data"]["RUSTFS_SECRET_KEY"], validate=True).decode()
    source = get("-n", "ani-system", "get", "secret", SECRET, "--ignore-not-found=true")
    ca = base64.b64decode(os.environ["MODELDEV_PUBLIC_S3_CA_BASE64"], validate=True)
    require(digest(ca) == CA_SHA, "PUBLIC_CA_SHA_MISMATCH")
    context = ssl.create_default_context(cadata=ca.decode())

    def request(method, path, query=None, body=b"", credentials=None, extra_headers=None):
        query = urllib.parse.urlencode(sorted((query or {}).items()), quote_via=urllib.parse.quote)
        timestamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        day = timestamp[:8]
        body_hash = digest(body)
        authority = HOST + ":9000"
        headers = {"host": authority, "x-amz-content-sha256": body_hash, "x-amz-date": timestamp}
        signed_headers = ";".join(sorted(headers))
        canonical_headers = "".join(k + ":" + headers[k] + "\n" for k in sorted(headers))
        canonical = "\n".join((method, path, query, canonical_headers, signed_headers, body_hash))
        scope = day + "/us-east-1/s3/aws4_request"
        message = "\n".join(("AWS4-HMAC-SHA256", timestamp, scope, digest(canonical.encode())))
        ak, sk = credentials if credentials else (access, secret)
        key = ("AWS4" + sk).encode()
        for part in (day, "us-east-1", "s3", "aws4_request"):
            key = hmac.new(key, part.encode(), hashlib.sha256).digest()
        signature = hmac.new(key, message.encode(), hashlib.sha256).hexdigest()
        headers["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + ak + "/" + scope + ", SignedHeaders=" + signed_headers + ", Signature=" + signature
        headers.update(extra_headers or {})
        c = http.client.HTTPSConnection(HOST, 9000, context=context, timeout=15)
        c._create_connection = lambda address, timeout, *a, **kw: socket.create_connection((service["spec"]["clusterIP"], 9000), timeout)
        try:
            c.request(method, path + ("?" + query if query else ""), body=body, headers=headers)
            r = c.getresponse()
            raw = r.read(1024 * 1024 + 1)
            require(len(raw) <= 1024 * 1024, "RESPONSE_TOO_LARGE")
            return r.status, raw
        finally:
            c.close()

    status, raw = request("GET", "/rustfs/admin/v3/list-canned-policies")
    require(status == 200 and isinstance(json.loads(raw), dict), "AUTHENTICATED_ADMIN_READ_FAILED")
    head, _ = request("HEAD", "/" + BUCKET)
    require(head in (200, 404), "BUCKET_ABSENCE_UNPROVEN")
    result = {"schema": "ani.modeldev.artifact-storage.preflight.v1",
        "observed_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "identity_match": True, "read_only": True, "credential_values_output": False,
        "endpoint": "https://" + HOST + ":9000", "tls_verified": True,
        "public_ca_sha256": CA_SHA, "service_uid": service["metadata"]["uid"],
        "service_vip": service["spec"]["clusterIP"], "deployment_uid": dep["metadata"]["uid"],
        "storage_images": [c["image"] for c in dep["spec"]["template"]["spec"]["containers"]],
        "admin_authenticated_read_http": status, "candidate_bucket": BUCKET,
        "candidate_bucket_head_http": head, "candidate_source_secret": SECRET,
        "candidate_source_secret_absent": source is None, "writes": 0}
    if sys.argv[1:] in (["--apply"], ["--complete-account"]):
        completing = sys.argv[1:] == ["--complete-account"]
        if completing:
            require(head == 200 and source and source["metadata"]["uid"] ==
                    "bc62d45a-d83c-4ca8-b5fa-ca07c6cadb47", "ORIGINAL_SOURCE_OR_BUCKET_MISMATCH")
        else:
            require(head == 404 and source is None, "FIXED_CANDIDATES_NOT_ABSENT")
        require(service["metadata"]["uid"] == "8efd9343-10e1-49c1-a777-2a6b22e646a9",
                "REVIEWED_STORAGE_SERVICE_UID_MISMATCH")
        result["provisioning"] = provision(request, access, source if completing else None)
        result["read_only"] = False
        result["writes"] = result["provisioning"]["writes"]
    else:
        require(not sys.argv[1:], "UNSUPPORTED_ARGUMENT")
    print(json.dumps(result))


def provision(request, expected_parent, original_source=None):
    """One finite platform bucket/identity; every mutation is journalled once."""
    root = Path("/home/kubercloud/tenantenv-20261101/artifact-storage")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(not root.is_symlink() and root.stat().st_uid == os.getuid()
            and root.stat().st_mode & 0o077 == 0, "PRIVATE_JOURNAL_OWNERSHIP_MISMATCH")
    writes = []

    def save(name, value):
        with (root / name).open("x") as out:
            os.chmod(root / name, 0o600)
            json.dump(value, out, sort_keys=True, indent=2)
            out.write("\n"); out.flush(); os.fsync(out.fileno())

    def once(name, operation):
        save(name + "-issued.json", {"phase": name, "issued_at_utc":
             datetime.datetime.now(datetime.timezone.utc).isoformat(),
             "unknown_outcome_requires_readback": True})
        status, body = operation()
        error_code = None
        if status >= 400:
            try:
                value = json.loads(body)
                error_code = value.get("Code", value.get("code"))
            except (ValueError, AttributeError):
                try:
                    error_code = next((x.text for x in ET.fromstring(body).iter()
                                       if x.tag.rsplit("}", 1)[-1] == "Code"), None)
                except ET.ParseError:
                    pass
            if not isinstance(error_code, str) or not re.fullmatch("[A-Za-z0-9_]{1,80}", error_code):
                error_code = "UNCLASSIFIED"
        save(name + "-response.json", {"http_status": status, "error_code": error_code})
        require(200 <= status < 300, name.upper() + "_REJECTED_OR_UNKNOWN_NO_RETRY")
        writes.append(name)
        return body

    arn = "arn:aws:s3:::" + BUCKET
    policy = {"Version": "2012-10-17", "Statement": [
        {"Effect": "Allow", "Action": ["s3:GetBucketLocation", "s3:ListBucket"], "Resource": [arn]},
        {"Effect": "Allow", "Action": ["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject",
         "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
         "Resource": [arn + "/artifacts/*"]}]}
    canonical = lambda v: json.dumps(v, sort_keys=True, separators=(",", ":")).encode()
    if original_source:
        require(json.loads((root / "reviewed-policy.json").read_text()) == policy,
                "ORIGINAL_POLICY_CHANGED")
        require(json.loads((root / "create-artifact-service-account-response.json").read_text()) ==
                {"http_status": 400, "error_code": "InvalidRequest"}, "ORIGINAL_ACCOUNT_WRITE_NOT_REJECTED")
        require(original_source["type"] == "Opaque" and set(original_source["data"]) == {"access_key", "secret_key"}
                and original_source["metadata"].get("labels", {}).get("app.kubernetes.io/managed-by") ==
                "modeldev-tenantenv-20261101", "ORIGINAL_SOURCE_SCHEMA_OR_OWNER_MISMATCH")
        ak, sk = (base64.b64decode(original_source["data"][k], validate=True).decode()
                  for k in ("access_key", "secret_key"))
        status, raw = request("GET", "/rustfs/admin/v3/list-service-accounts", {"user": expected_parent})
        require(status == 200, "ORIGINAL_ACCOUNT_INVENTORY_FAILED")
        accounts = json.loads(raw)["accounts"]
        require(isinstance(accounts, list) and all(x.get("accessKey") != ak for x in accounts),
                "REJECTED_ACCOUNT_IS_NOT_ABSENT")
        save("short-name-correction.json", {"old_name": "modeldev-tenantenv-artifacts-20261101",
            "new_name": "md-tenantenv-artifacts-20261101", "account_absence_readback": True,
            "same_original_source_secret_uid": original_source["metadata"]["uid"],
            "policy_and_credentials_unchanged": True,
            "source": "https://github.com/rustfs/rustfs/blob/d47f54bfb2f39f48bd1adda334bd27e151fe85b8/crates/madmin/src/user.rs#L309-L339",
            "reason": "The original requested display name exceeds the native 32 character maximum."})
    else:
        save("reviewed-policy.json", policy)
        ak, sk = "MDENV" + secrets.token_hex(10).upper(), secrets.token_urlsafe(24)
    manifest = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": SECRET,
        "namespace": "ani-system", "labels": {"app.kubernetes.io/managed-by": "modeldev-tenantenv-20261101"}},
        "type": "Opaque", "data": {"access_key": base64.b64encode(ak.encode()).decode(),
        "secret_key": base64.b64encode(sk.encode()).decode()}}

    def create_source():
        p = subprocess.run(["kubectl", "--context", CTX, "--request-timeout=15s", "create", "-f", "-", "-o", "json"],
                            input=canonical(manifest), capture_output=True, timeout=25)
        require(p.returncode == 0, "SOURCE_SECRET_CREATE_REJECTED_OR_UNKNOWN")
        obj = json.loads(p.stdout)
        require(obj["data"] == manifest["data"] and obj["type"] == "Opaque", "SOURCE_SECRET_READBACK_MISMATCH")
        save("source-secret-identity.json", {"namespace": "ani-system", "name": SECRET,
             "uid": obj["metadata"]["uid"], "resourceVersion": obj["metadata"]["resourceVersion"]})
        return 201, b""

    if not original_source:
        once("create-source-secret", create_source)
        once("create-dedicated-bucket", lambda: request("PUT", "/" + BUCKET))
        versioning = b'<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>'
        once("enable-bucket-versioning", lambda: request("PUT", "/" + BUCKET, {"versioning": ""}, versioning,
             extra_headers={"Content-Type": "application/xml"}))
    status, raw = request("GET", "/" + BUCKET, {"versioning": ""})
    require(status == 200 and any(x.tag.rsplit("}", 1)[-1] == "Status" and x.text == "Enabled"
            for x in ET.fromstring(raw).iter()), "VERSIONING_READBACK_FAILED")
    body = {"policy": canonical(policy).decode(), "expiration": None,
            "name": "md-tenantenv-artifacts-20261101", "accessKey": ak, "secretKey": sk}
    raw = once("create-artifact-service-account-short-name" if original_source else "create-artifact-service-account", lambda: request("PUT", "/rustfs/admin/v3/add-service-accounts",
               body=canonical(body), extra_headers={"Content-Type": "application/json"}))
    creds = json.loads(raw)["credentials"]
    require(creds.get("accessKey") == ak and creds.get("secretKey") == sk and not creds.get("sessionToken"),
            "ACCOUNT_CREATE_REPLY_MISMATCH")
    status, raw = request("GET", "/rustfs/admin/v3/info-access-key", {"accessKey": ak})
    require(status == 200, "ACCOUNT_READBACK_FAILED")
    info = json.loads(raw)
    actual_policy = info.get("policy")
    if isinstance(actual_policy, str):
        actual_policy = json.loads(actual_policy)

    def semantic(value):
        require(set(value) == {"Version", "Statement"} and value["Version"] == "2012-10-17", "POLICY_SHAPE_MISMATCH")
        statements = []
        for s in value["Statement"]:
            require(set(s) == {"Effect", "Action", "Resource"}, "POLICY_STATEMENT_SHAPE_MISMATCH")
            statements.append({"Effect": s["Effect"], **{k: sorted(s[k] if isinstance(s[k], list) else [s[k]])
                               for k in ("Action", "Resource")}})
        return sorted(statements, key=lambda s: canonical(s))

    require(info.get("accessKey") == ak and info.get("parentUser") == expected_parent
            and info.get("userType") == "Service Account"
            and info.get("accountStatus") == "on" and info.get("impliedPolicy") is False
            and semantic(actual_policy) == semantic(policy), "SCOPED_ACCOUNT_IDENTITY_OR_POLICY_MISMATCH")
    # Read-only negative checks cannot accidentally create objects elsewhere.
    negative = []
    for bucket in ("ani-md-v21-input-01a15f47", "ani-md-v21-output-01a15f47", "ani-models-v21-01a15f47"):
        status, _ = request("GET", "/" + bucket + "/modeldev-artifact-denial-probe", credentials=(ak, sk))
        require(status == 403, "OTHER_BUCKET_ACCESS_NOT_DENIED")
        negative.append({"bucket": bucket, "get_http": status})
    status, _ = request("GET", "/rustfs/admin/v3/list-canned-policies", credentials=(ak, sk))
    require(status == 403, "ARTIFACT_ACCOUNT_HAS_ADMIN_ACCESS")
    result = {"status": "PROVISIONED_POLICY_READBACK_AND_CROSS_BUCKET_DENIAL_PASS",
        "bucket": BUCKET, "versioning": "Enabled", "source_secret": json.loads((root / "source-secret-identity.json").read_text()),
        "policy_sha256": digest(canonical(policy)), "explicit_bucket_only_account": True,
        "shared_platform_identity_not_tenant_iam": True, "other_bucket_denials": negative,
        "admin_api_denial_http": status, "credential_values_output": False, "writes": len(writes),
        "phases": writes, "launcher_io": "NOT_RUN", "credential_rotation": "NOT_RUN"}
    save("provisioned-receipt.json", result)
    return result


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        applying = sys.argv[1:] in (["--apply"], ["--complete-account"])
        print(json.dumps({"status": "BLOCKED", "read_only": not applying,
            "reason": str(e) if type(e) is RuntimeError else type(e).__name__,
            "writes": "POSSIBLE_READ_FIXED_NODE_JOURNAL_BEFORE_ANY_RETRY" if applying else 0}))
        raise SystemExit(1)
