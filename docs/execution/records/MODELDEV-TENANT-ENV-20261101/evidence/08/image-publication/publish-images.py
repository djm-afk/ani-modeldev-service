#!/usr/bin/env python3
"""Fedora-only fixed d807 image publication; credentials arrive only on stdin."""
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

AUTHORITY = "10.10.1.66:30003"
PROJECT = "ani-resource-factor10-20261022"
REVISION = "d807cbdbd5dd6664d61480acedc752e19cfca48a"
TREE = "141053367fa4be9a6fb9c764b2d7900b5282be20"
INPUTS = "dd70433a47d201f2cb8343a6e643ef38223f90dd88a78c79ad590286f37678a9"
WORK = Path("/home/chabking/tenantenv-20261101/images-d807-cymq2rk8")
RUN = WORK / "publication-20261101"
PODMAN = ["podman", "--root", "/home/chabking/workspace/modeldev-open-limits-20261028-01a182e4/runtime-image-storage", "--runroot", "/run/user/1000/modeldev-open-limits-image-storage", "--tmpdir", "/home/chabking/workspace/modeldev-open-limits-20261028-01a182e4/runtime-image-tmp", "--storage-driver", "overlay"]
IMAGES = {
    "step": "102a17fcca37414302ee9703e310bae554905349d8f3b6556ac323ea1408a2a9",
    "webhook": "c1eb9355492835c7cb4897e01fb5b7c400fad91235e0910024c2ded76725e252",
}


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def atomic_json(path, value):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    os.replace(temporary, path)


def command(args, log, timeout=60):
    started = time.monotonic()
    with log.open("wb") as out:
        result = subprocess.run(args, stdout=out, stderr=subprocess.STDOUT, timeout=timeout)
    return {"exit": result.returncode, "seconds": round(time.monotonic() - started, 3), "log": str(log), "log_bytes": log.stat().st_size, "log_sha256": hashlib.sha256(log.read_bytes()).hexdigest()}


def main():
    import sys
    credentials = json.load(sys.stdin)
    assert credentials["identity_match"] is True
    assert credentials["authority"] == AUTHORITY and credentials["project"] == PROJECT
    ca = base64.b64decode(credentials["ca_pem_b64"], validate=True)
    assert hashlib.sha256(ca).hexdigest() == "65f1a1d14d21fed31b89f25b143e78b16605b82d32c56ef6bf8167fe78bab871"
    os.umask(0o077)
    RUN.mkdir(mode=0o700, exist_ok=True)
    receipt_path = RUN / "publication-receipt.json"
    receipt = {"source_commit": REVISION, "source_tree": TREE, "source_inputs_sha256": INPUTS, "started_at": now(), "authority": AUTHORITY, "project": PROJECT, "credential_source": credentials["public_source"], "images": {}, "rollout": "NOT_RUN", "credentials_emitted": False}
    private = Path(tempfile.mkdtemp(prefix="registry-auth-", dir=RUN))
    try:
        certdir = private / "certs"
        certdir.mkdir(mode=0o700)
        (certdir / "ca.crt").write_bytes(ca)
        auth = private / "auth.json"
        encoded = base64.b64encode((credentials["username"] + ":" + credentials["password"]).encode()).decode()
        auth.write_text(json.dumps({"auths": {AUTHORITY: {"auth": encoded}}}))
        auth.chmod(0o600)
        options = ["--authfile", str(auth), "--cert-dir", str(certdir), "--tls-verify=true"]
        for role, image_id in IMAGES.items():
            image = json.loads(subprocess.check_output(PODMAN + ["image", "inspect", image_id]))[0]
            labels = image["Config"]["Labels"]
            assert image["Id"] == image_id
            assert labels["org.opencontainers.image.revision"] == REVISION
            assert labels["io.ani.source-tree"] == TREE
            assert labels["io.ani.build-inputs-sha256"] == INPUTS
            target = f"{AUTHORITY}/{PROJECT}/ani-modeldev-{role}:tenantenv-d807cbdbd5dd-cymq2rk8"
            item = {"state": "PREFLIGHT", "source_image_id": image_id, "source_local_size_bytes": image["Size"], "source_labels": {k: labels[k] for k in ("org.opencontainers.image.revision", "io.ani.source-tree", "io.ani.build-inputs-sha256")}, "target": target}
            receipt["images"][role] = item
            atomic_json(receipt_path, receipt)
            prior = subprocess.run(["skopeo", "inspect", *options, "--raw", "docker://" + target], capture_output=True, timeout=30)
            if prior.returncode == 0:
                manifest = prior.stdout
                item["state"] = "EXISTING_TARGET_REQUIRES_MATCH"
            else:
                error = prior.stderr.decode(errors="replace").lower()
                assert "manifest unknown" in error or "name unknown" in error, "REGISTRY_PREFLIGHT_NOT_PROVEN_ABSENT"
                digestfile = RUN / (role + ".registry-digest")
                item["state"] = "PUSH_ISSUED"
                atomic_json(receipt_path, receipt)
                item["push"] = command(PODMAN + ["push", *options, "--format=oci", "--digestfile", str(digestfile), image_id, "docker://" + target], RUN / (role + ".push.log"), timeout=180)
                assert item["push"]["exit"] == 0, "PUSH_FAILED"
                manifest = subprocess.check_output(["skopeo", "inspect", *options, "--raw", "docker://" + target], timeout=30)
                assert digestfile.read_text().strip() == "sha256:" + hashlib.sha256(manifest).hexdigest(), "PUSH_DIGEST_READBACK_MISMATCH"
            config = json.loads(subprocess.check_output(["skopeo", "inspect", *options, "--config", "docker://" + target], timeout=30))
            remote_labels = config["config"]["Labels"]
            assert all(remote_labels.get(k) == v for k, v in item["source_labels"].items()), "REMOTE_LABEL_MISMATCH"
            parsed = json.loads(manifest)
            assert parsed["config"]["digest"] == "sha256:" + image_id, "REMOTE_CONFIG_IMAGE_ID_MISMATCH"
            item.update(state="PUBLISHED_VERIFIED", registry_digest="sha256:" + hashlib.sha256(manifest).hexdigest(), manifest_bytes=len(manifest), registry_config=parsed["config"], layers=parsed["layers"], compressed_layer_bytes=sum(v["size"] for v in parsed["layers"]), user=config["config"].get("User"), remote_labels={k: remote_labels[k] for k in item["source_labels"]})
            atomic_json(receipt_path, receipt)
        receipt["status"] = "SMALL_IMAGES_PUBLISHED_VERIFIED"
    except Exception as error:
        receipt["status"] = "FAILED_OR_UNCERTAIN_READ_EXACT_TARGET_BEFORE_RETRY"
        receipt["error_class"] = type(error).__name__
        receipt["finite_reason"] = str(error) if isinstance(error, AssertionError) else "IMAGE_PUBLICATION_COMMAND_FAILURE"
    finally:
        shutil.rmtree(private)
        receipt["temporary_auth_and_ca_removed"] = not private.exists()
        receipt["finished_at"] = now()
        atomic_json(receipt_path, receipt)
    print(json.dumps(receipt))
    return 0 if receipt["status"] == "SMALL_IMAGES_PUBLISHED_VERIFIED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
