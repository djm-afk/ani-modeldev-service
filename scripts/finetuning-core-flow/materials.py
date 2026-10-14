#!/usr/bin/env python3
"""Create task-only private TLS and bounded, versioned RustFS materials on Fedora."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import uuid

from deploy import Cluster, OWNER, SYSTEM, TENANT, encoded, obj, save


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def certificate_materials(cluster, root):
    private = root / ".private"
    private.mkdir(mode=0o700, exist_ok=True)
    ca, key, leaf, leafkey = [private / n for n in ["task-ca.crt", "task-ca.key", "modeldev.crt", "modeldev.key"]]
    def openssl(args):
        r = subprocess.run(["openssl", *args], capture_output=True, timeout=30)
        if r.returncode:
            raise RuntimeError("task certificate operation failed; private output suppressed")
    if not ca.exists():
        openssl(["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(ca), "-days", "90", "-subj", "/CN=finetuning-core-flow-20261014-task-CA", "-addext", "basicConstraints=critical,CA:TRUE"])
    if not leaf.exists():
        csr = private / "modeldev.csr"
        extensions = private / "modeldev.ext"
        extensions.write_text("subjectAltName=DNS:ani-modeldev-service,DNS:modeldev." + SYSTEM + ".svc,DNS:modeldev." + SYSTEM + ".svc.cluster.local\nextendedKeyUsage=serverAuth\n")
        openssl(["req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", str(leafkey), "-out", str(csr), "-subj", "/CN=ani-modeldev-service"])
        openssl(["x509", "-req", "-in", str(csr), "-CA", str(ca), "-CAkey", str(key), "-CAcreateserial", "-out", str(leaf), "-days", "30", "-extfile", str(extensions)])
    for name, ns, secret in [("s3-ca.crt", "ani-platform", "ani-rustfs-tls"), ("kfp-ca.crt", "kubeflow", "ani-kfp-ca")]:
        public = cluster.get("secret", secret, ns)["data"]["ca.crt"]
        (private / name).write_bytes(base64.b64decode(public))
    cluster.apply(obj("Secret", "core-flow-modeldev-tls", SYSTEM, type="Opaque", data={"ca.crt": base64.b64encode(ca.read_bytes()).decode(), "tls.crt": base64.b64encode(leaf.read_bytes()).decode(), "tls.key": base64.b64encode(leafkey.read_bytes()).decode()}))


def provision_storage(cluster, root, cli, source):
    private = root / ".private"
    statepath = private / "s3-state.json"
    suffix = secrets.token_hex(4)
    state = json.loads(statepath.read_bytes()) if statepath.exists() else {
        "owner": OWNER, "input_bucket": "ani-ft-core-input-20261014-" + suffix, "publication_bucket": "ani-ft-core-output-20261014-" + suffix, "kfp_bucket": "ani-ft-core-kfp-20261014-" + suffix, "tenant_id": str(uuid.uuid4()), "base_model_id": str(uuid.uuid4()), "base_version_id": str(uuid.uuid4()), "input_version_id": str(uuid.uuid4()), "import_request_id": str(uuid.uuid4()), "users": {n: {"access_key_id": "ft14" + n + secrets.token_hex(5), "secret_access_key": secrets.token_urlsafe(32)} for n in ["control", "kfp", "readback"]}, "operations": {}}
    if state["owner"] != OWNER:
        raise RuntimeError("storage state ownership mismatch")
    save(statepath, state)
    materials = json.loads((source / "candidate-materials.json").read_bytes())
    data, base = source / "finetune_data.json", source / "qwen2.5-0.5b-instruct.tar"
    if digest(data) != materials["input_sha256"] or digest(base) != materials["base_archive_sha256"] or data.stat().st_size != int(materials["input_bytes"]) or base.stat().st_size != int(materials["base_archive_bytes"]):
        raise RuntimeError("approved workload material bytes mismatch")
    root_secret = cluster.get("secret", "ani-rustfs-root", "ani-platform")["data"]
    config = private / "rc"
    config.mkdir(mode=0o700, exist_ok=True)
    # The public RustFS certificate explicitly includes 127.0.0.1. This local
    # forwarding address is not a guessed cluster endpoint or permanent service.
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    forward = subprocess.Popen(cluster.base + ["-n", "ani-platform", "port-forward", "--address", "127.0.0.1", "service/ani-rustfs-svc", str(port) + ":9000"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    aliases = {"root": {"access_key_id": base64.b64decode(root_secret["RUSTFS_ACCESS_KEY"]).decode(), "secret_access_key": base64.b64decode(root_secret["RUSTFS_SECRET_KEY"]).decode()}, **state["users"]}
    fields = []
    for name, credentials in aliases.items():
        values = {"name": name, "endpoint": "https://127.0.0.1:" + str(port), "access_key": credentials["access_key_id"], "secret_key": credentials["secret_access_key"], "region": "us-east-1", "signature": "v4", "bucket_lookup": "path", "insecure": False, "ca_bundle": str(private / "s3-ca.crt")}
        fields.append("[[aliases]]\n" + "\n".join(k + " = " + json.dumps(v) for k, v in values.items()))
    (config / "config.toml").write_text("schema_version = 1\n" + "\n".join(fields) + "\n")
    environment = dict(os.environ, RC_CONFIG_DIR=str(config))
    def rc(args, name=None):
        if name:
            old = state["operations"].get(name)
            if old and old["state"] == "CONFIRMED":
                return (private / (name + ".out")).read_bytes()
            if old:
                raise RuntimeError("reconcile retained storage operation before retry: " + name)
            state["operations"][name] = {"state": "PENDING"}
            save(statepath, state)
        r = subprocess.run([str(cli), "--json", "--no-progress", *args], capture_output=True, env=environment, timeout=900)
        if name:
            (private / (name + ".out")).write_bytes(r.stdout + r.stderr)
            state["operations"][name] = {"state": "CONFIRMED" if r.returncode == 0 else "FAILED", "exit_code": r.returncode}
            save(statepath, state)
        if r.returncode:
            raise RuntimeError("RustFS operation failed; private output suppressed: " + (name or args[0]))
        return r.stdout
    try:
        for _ in range(100):
            if forward.poll() is not None:
                raise RuntimeError("bounded RustFS forwarding failed")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("RustFS forwarding did not start")
        for purpose in ["input", "publication", "kfp"]:
            rc(["bucket", "create", "root/" + state[purpose + "_bucket"], "--with-versioning"], "create-" + purpose + "-bucket")
        def statement(actions, bucket, prefix):
            return {"Effect": "Allow", "Action": actions, "Resource": ["arn:aws:s3:::" + bucket + "/" + prefix + "*"]}
        policies = {
            "control": [statement(["s3:GetObject", "s3:GetObjectVersion"], state["input_bucket"], "approved/"), statement(["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"], state["publication_bucket"], "executions/")],
            "kfp": [statement(["s3:GetObject", "s3:GetObjectVersion", "s3:PutObject"], state["kfp_bucket"], "artifacts/")],
            "readback": [statement(["s3:GetObject", "s3:GetObjectVersion"], state["publication_bucket"], "executions/")],
        }
        for purpose, statements in policies.items():
            policy = private / (purpose + "-policy.json")
            save(policy, {"Version": "2012-10-17", "Statement": statements})
            name = "ft-core-20261014-" + purpose
            rc(["admin", "policy", "create", "root", name, str(policy)], "create-" + purpose + "-policy")
            credentials = state["users"][purpose]
            rc(["admin", "user", "add", "root", credentials["access_key_id"], credentials["secret_access_key"]], "create-" + purpose + "-user")
            rc(["admin", "policy", "attach", "root", name, "--user", credentials["access_key_id"]], "attach-" + purpose + "-policy")
        rc(["put", str(data), "root/" + state["input_bucket"] + "/approved/data/finetune_data.json"], "upload-approved-data")
        rc(["put", str(base), "root/" + state["input_bucket"] + "/approved/base/qwen.tar"], "upload-approved-base")
        observations = {}
        for purpose, key in [("input", "approved/data/finetune_data.json"), ("base", "approved/base/qwen.tar")]:
            raw = rc(["stat", "root/" + state["input_bucket"] + "/" + key])
            (private / (purpose + "-head.json")).write_bytes(raw)
            observations[purpose] = json.loads(raw)
        bindings = [{"tenant_id": state["tenant_id"], "purpose": purpose, "connection_id": "ft-core-s3-v1", "bucket": bucket, "prefix": prefix} for purpose, bucket, prefix in [("prepare", state["input_bucket"], "approved/data"), ("prepare-base-model", state["input_bucket"], "approved/base"), ("publish", state["publication_bucket"], "executions")]]
        control = {**state["users"]["control"], "sts_bindings": bindings}
        save(private / "control.json", control)
        save(private / "readback.json", state["users"]["readback"])
        cluster.apply(obj("Secret", "core-flow-storage", SYSTEM, type="Opaque", data={"control.json": base64.b64encode(encoded(control)).decode(), "readback.json": base64.b64encode(encoded(state["users"]["readback"])).decode()}))
        cluster.apply(obj("Secret", "mlpipeline-minio-artifact", TENANT, type="Opaque", data={"accesskey": base64.b64encode(state["users"]["kfp"]["access_key_id"].encode()).decode(), "secretkey": base64.b64encode(state["users"]["kfp"]["secret_access_key"].encode()).decode()}))
        result = {"owner": OWNER, "tenant_id": state["tenant_id"], "base_model_id": state["base_model_id"], "base_version_id": state["base_version_id"], "input_version_id": state["input_version_id"], "import_request_id": state["import_request_id"], "input_bucket": state["input_bucket"], "publication_bucket": state["publication_bucket"], "kfp_bucket": state["kfp_bucket"], "source": materials, "object_heads": observations, "storage_connection_id": "ft-core-s3-v1", "credentials_reference": SYSTEM + "/core-flow-storage", "identity_policy_files": [str(private / (p + "-policy.json")) for p in policies], "ready_generated": False}
        save(root / "storage-receipt.json", result)
        print(json.dumps({"phase": "TASK_STORAGE_MATERIALS_CREATED", "receipt": str(root / "storage-receipt.json"), "ready_generated": False}))
    finally:
        forward.terminate()
        try:
            forward.wait(timeout=5)
        except subprocess.TimeoutExpired:
            forward.kill()
            forward.wait(timeout=5)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--root", type=Path, required=True)
    p.add_argument("--kubectl", required=True)
    p.add_argument("--kubeconfig", required=True)
    p.add_argument("--rustfs-cli", type=Path, required=True)
    p.add_argument("--workload-materials", type=Path, required=True)
    args = p.parse_args()
    if not Path("/etc/fedora-release").exists():
        raise SystemExit("execute only on Fedora")
    os.umask(0o077)
    cluster = Cluster(args.kubectl, args.kubeconfig)
    cluster.guard()
    for n in [SYSTEM, TENANT]:
        if cluster.get("namespace", n)["metadata"]["labels"].get("ani.io/managed-by") != OWNER:
            raise SystemExit("task namespace owner mismatch")
    certificate_materials(cluster, args.root)
    provision_storage(cluster, args.root, args.rustfs_cli, args.workload_materials)


if __name__ == "__main__":
    main()
