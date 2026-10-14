#!/usr/bin/env python3
"""Fedora-only one-shot application driver; KFP advances all normal tasks.

Requires actual immutable images, canonical Release and confirmed PipelineVersion.
No placeholder Release is enabled. No READY or success rows are inserted here.
"""
import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import uuid

from deploy import Cluster, OWNER, SYSTEM, TENANT, encoded, obj, save

NAMESPACE_UID = "8f79e7cc-769b-4b2d-87d3-a69c53c946f3"
SA_DIRECTORY = "/var/run/secrets/kubernetes.io/serviceaccount"


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def immutable(value):
    if not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", value):
        raise ValueError("explicit immutable image required")
    return value


def guard(cluster):
    cluster.guard()
    for name in (SYSTEM, TENANT):
        value = cluster.get("namespace", name)
        if value["metadata"]["labels"].get("ani.io/managed-by") != OWNER or name == TENANT and value["metadata"]["uid"] != NAMESPACE_UID:
            raise ValueError("task namespace identity mismatch")


def pipeline(cluster, root, path):
    from kfp import Client
    ledger = root / "pipeline-receipt.json"
    if ledger.exists():
        raise ValueError("inspect retained upload phase; never blindly choose another Pipeline ID")
    ca = root / ".private/kfp-entry-ca.crt"
    ca.write_bytes(base64.b64decode(cluster.get("secret", "ani-kfp-entry-tls", "kubeflow")["data"]["ca.crt"]))
    token = cluster.run(["create", "token", "modeldev-control", "-n", SYSTEM, "--audience=pipelines.kubeflow.org", "--duration=1h"]).decode().strip()
    client = Client(host="https://172.16.101.10:30445", namespace=TENANT, existing_token=token, ssl_ca_cert=str(ca), verify_ssl=True)
    record = {"namespace": TENANT, "namespace_uid": NAMESPACE_UID, "pipeline_ir_sha256": sha(path.read_bytes()), "source_reference": str(path), "phase": "UPLOAD_PIPELINE_PENDING"}
    save(ledger, record)
    created = client.upload_pipeline(str(path), pipeline_name=OWNER, namespace=TENANT)
    record.update(pipeline_id=created.pipeline_id, phase="UPLOAD_VERSION_PENDING")
    save(ledger, record)
    version = client.upload_pipeline_version(str(path), pipeline_version_name="ft-core-" + record["pipeline_ir_sha256"][:12], pipeline_id=created.pipeline_id)
    record.update(pipeline_version_id=version.pipeline_version_id, phase="CREATE_EXPERIMENT_PENDING")
    save(ledger, record)
    experiment = client.create_experiment(name=OWNER, namespace=TENANT)
    record.update(experiment_id=experiment.experiment_id, phase="READBACK_PENDING")
    save(ledger, record)
    if client.get_pipeline_version(record["pipeline_id"], record["pipeline_version_id"]).pipeline_version_id != record["pipeline_version_id"]:
        raise ValueError("PipelineVersion readback identity mismatch")
    record["phase"] = "CONFIRMED"
    save(ledger, record)
    print(json.dumps({"phase": "TASK_PIPELINE_CONFIRMED", "receipt": str(ledger), "pipeline_id": record["pipeline_id"], "pipeline_version_id": record["pipeline_version_id"]}))


def render(cluster, args):
    root = args.root
    source_commit = args.source_commit
    if not re.fullmatch(r"[0-9a-f]{40}", source_commit or ""):
        raise ValueError("exact source commit required")
    driver_image = immutable(args.driver_image)
    component_image = immutable(args.component_image)
    storage = json.loads((root / "storage-receipt.json").read_bytes())
    upload = json.loads((root / "pipeline-receipt.json").read_bytes())
    release_raw = args.release.read_bytes()
    release = json.loads(release_raw)
    if upload["phase"] != "CONFIRMED" or upload["namespace_uid"] != NAMESPACE_UID or any(release[key] != upload[key] for key in ("pipeline_id", "pipeline_version_id", "pipeline_ir_sha256")) or release["kind"] != "FINETUNING" or release["execution_timeout_seconds"] != 10800 or int(release["workspace"]["capacity_bytes"]) != 20 << 30 or release["finetuning"]["base_model_version_id"] != storage["base_version_id"]:
        raise ValueError("candidate must bind actual PipelineVersion, approved base and frozen budget")
    state_path = root / ".private/driver-state.json"
    state = json.loads(state_path.read_bytes()) if state_path.exists() else {"owner": OWNER, "binding_id": str(uuid.uuid4()), "input_requested_at": now(), "scenarios": {}}
    if state["owner"] != OWNER:
        raise ValueError("driver state owner mismatch")
    if args.scenario in state["scenarios"]:
        raise ValueError("scenario already frozen; use retained manifest and original operation/execution")
    raw_token = cluster.run(["create", "token", "modeldev-step", "-n", TENANT, "--duration=10m"]).decode().strip()
    claims = json.loads(base64.urlsafe_b64decode(raw_token.split(".")[1] + "=="))
    audience = claims["aud"]
    if not isinstance(audience, list) or len(audience) != 1:
        raise ValueError("one verified managed step audience required")
    materials = storage["source"]
    tenant = storage["tenant_id"]
    step_configuration = {"tenant_id": tenant, "namespace_uid": NAMESPACE_UID, "context_file": "", "token_file": SA_DIRECTORY + "/token", "grpc_target": "modeldev." + SYSTEM + ".svc:9001", "grpc_server_name": "ani-modeldev-service", "grpc_ca_file": "/etc/modeldev-step/ca.crt", "workspace_directory": "/workspace", "pvc_name": "", "inventory_file": "/workspace/publication/inventory.json", "candidate_file": "/tmp/publication.json", "task_id": "", "poll_interval_seconds": 1, "timeout_seconds": 10800, "s3": {"endpoint": "https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000", "region": "us-east-1", "ca_file": "/etc/modeldev-step/s3-ca.crt", "credentials_file": ""}}
    step_configuration["s3"]["streaming_timeout_seconds"] = 900
    proof = {"cluster_uid": "5277649d-e28d-4a0a-ae76-8354365c63bc", "namespace_uid": NAMESPACE_UID, "pipeline": upload, "foundation_receipt_sha256": sha((root / "foundation-receipt.json").read_bytes()), "foundation_adjustment_sha256": sha((root / "foundation-adjustment-receipt.json").read_bytes()), "storage_receipt_sha256": sha((root / "storage-receipt.json").read_bytes()), "component_image": component_image, "driver_image": driver_image, "source_commit": source_commit, "step_audience": audience[0]}
    proof_raw = encoded(proof)
    environment = {"binding_id": state["binding_id"], "binding_digest": sha(proof_raw), "cluster_id": "ani-lab", "namespace_name": TENANT, "namespace_uid": NAMESPACE_UID, "kfp_connection_ref": "ft-core-kfp-v1", "experiment_id": upload["experiment_id"], "identities": {"modeldev_control_identity_ref": SYSTEM + "/modeldev-control", "kfp_step_service_account": "modeldev-step", "trainer_service_account": "trainer-workload", "verifier_service_account": "verifier", "tenant_proxy_identity": "ft-core-tenant-proxy-v1"}}
    binding = {"tenant_id": tenant, "environment": environment, "owner": {"reference": TENANT + "/modeldev-step-owner", "revision_sha256": sha(encoded(step_configuration)), "pipeline_root": "s3://" + storage["kfp_bucket"] + "/artifacts"}}
    scope = lambda purpose, prefix: {"storage_connection_id": storage["storage_connection_id"], "bucket": storage[purpose + "_bucket"], "approved_prefix": prefix, "credential_reference": "ft-core-" + purpose + "-v1"}
    fixed = lambda key, head, digest: {"storage_connection_id": storage["storage_connection_id"], "bucket": storage["input_bucket"], "key": key, "version_id": head["version_id"], "size_bytes": str(head["size_bytes"]), "sha256": digest}
    input_scope = scope("input", "approved/data")
    base_scope = scope("input", "approved/base")
    base_scope["credential_reference"] = "ft-core-base-read-v1"
    base = {"model_id": storage["base_model_id"], "version_id": storage["base_version_id"], "family": "Qwen2.5-0.5B-Instruct", "format": "safetensors", "object": fixed("approved/base/qwen.tar", storage["object_heads"]["base"], materials["base_archive_sha256"]), "scope": base_scope, "files": [{"path": x["path"], "size_bytes": x["bytes"], "sha256": x["sha256"]} for x in materials["base_files"]]}
    digest = sha(release_raw)
    evidence = {"reference": str(root / "driver-environment.json"), "sha256": sha(proof_raw)}
    facts = {"schema_version": "ani.modeldev.managed-admission-facts.v1", "resource_tenant_id": tenant, "release_id": release["release_id"], "release_digest": digest, "environment": environment, "input_scope": input_scope, "publication_scope": scope("publication", "executions"), "runtime": release["runtime"], "workspace": release["workspace"], "input_file_path": "/workspace/inputs/finetune_data.json", "output_directory_path": "/workspace/training", "environment_evidence": evidence, "application_evidence": evidence}
    bootstrap = {"server": {"grpc": {"network": "tcp", "addr": "0.0.0.0:9000", "timeout": "30s"}, "admin": {"network": "tcp", "addr": "0.0.0.0:9002", "timeout": "5s"}, "shutdown_timeout": "10s"}, "command": {"database_url_file": "/database/database.dsn", "client_ca_file": "/tls/ca.crt", "certificate_file": "/tls/tls.crt", "private_key_file": "/tls/tls.key", "governance_dns_name": "ani-governance", "admission_resolution": {"catalogue_directory": "/catalogue", "facts_files": [{"path": "/materials/facts.json", "sha256": sha(encoded(facts))}]}}, "runtime": {"step": {"network": "tcp", "addr": "0.0.0.0:9001", "timeout": "30s"}, "certificate_file": "/tls/tls.crt", "private_key_file": "/tls/tls.key", "kubernetes": {"endpoint": "https://kubernetes.default.svc", "ca_file": SA_DIRECTORY + "/ca.crt", "token_file": SA_DIRECTORY + "/token"}, "pipeline": {"endpoint": "https://ani-kfp-entry.kubeflow.svc:8443", "ca_file": "/public/kfp-ca.crt", "token_file": "/pipeline-identity/token"}, "object_storage": {"endpoint": "https://ani-rustfs-svc.ani-platform.svc.cluster.local:9000", "ca_file": "/public/s3-ca.crt", "region": "us-east-1", "credentials_file": "/storage/control.json", "connection_id": storage["storage_connection_id"], "max_object_bytes": 3 << 30}, "binding_file": "/config/binding.json", "binding_sha256": sha(encoded(binding)), "token_audience": audience[0], "api_timeout": "30s", "dispatch_interval": "1s", "dispatch_batch_size": 1}}
    bootstrap["runtime"]["step"]["timeout"] = "600s"
    bootstrap["runtime"]["object_storage"]["streaming_timeout"] = "600s"
    bootstrap["runtime"]["object_storage"]["max_object_bytes"] = int(release["output_contract"]["max_tar_bytes"])
    accepted = datetime.datetime.now(datetime.timezone.utc)
    if args.scenario == "deadline":
        if not 30 <= args.remaining_seconds <= 3600:
            raise ValueError("deadline needs a measured 30..3600 second remaining window")
        accepted -= datetime.timedelta(seconds=10800 - args.remaining_seconds)
    accepted_at = accepted.isoformat(timespec="microseconds").replace("+00:00", "Z")
    operation, execution = str(uuid.uuid4()), str(uuid.uuid4())
    intent = {"name": "CORE FLOW " + args.scenario, "kind": "FINETUNING", "preset_id": release["preset_id"], "dataset_version_id": storage["input_version_id"], "base_model_version_id": storage["base_version_id"], "finetuning": {"learning_rate": "0.0002", "max_steps": 10} if args.scenario == "b" else {}, "new_model_name": "ft-core-" + args.scenario}
    fixture = {"schema": "ani.finetuning.core-flow-fixture.v1", "bootstrap": bootstrap, "scenario": args.scenario, "tenant_id": tenant, "actor": "core-flow:test-driver", "operation_id": operation, "execution_id": execution, "intent": intent, "release_id": release["release_id"], "release_digest": digest, "canonical_release": release, "input": {"Format": "JSON", "TenantID": tenant, "RequestID": storage["import_request_id"], "InputVersionID": storage["input_version_id"], "Actor": "core-flow:test-driver", "RequestedAt": state["input_requested_at"], "Scope": input_scope, "Object": fixed("approved/data/finetune_data.json", storage["object_heads"]["input"], materials["input_sha256"])}, "base_model": base, "accepted_at": accepted_at, "stop_when_training": args.scenario == "stop", "restart_when_training": args.scenario == "restart", "expect_deadline": args.scenario == "deadline", "evidence_directory": "/evidence", "source_commit": source_commit, "material_source": str(root / "storage-receipt.json")}
    name = "core-flow-" + args.scenario
    private = root / ".private"
    config_rows = [obj("ConfigMap", "modeldev-step-owner", TENANT, data={"config.json": encoded(step_configuration).decode(), "ca.crt": (private / "task-ca.crt").read_text(), "s3-ca.crt": (private / "s3-ca.crt").read_text()}), obj("ConfigMap", "ani-kfp-ca", TENANT, data={"ca.crt": (private / "kfp-ca.crt").read_text()}), obj("Secret", "ani-kfp-ca", TENANT, type="Opaque", data={"ca.crt": base64.b64encode((private / "kfp-ca.crt").read_bytes()).decode()}), obj("Role", "modeldev-step", TENANT, rules=[{"apiGroups": [""], "resources": ["pods"], "verbs": ["get"]}, {"apiGroups": [""], "resources": ["persistentvolumeclaims"], "verbs": ["get", "create", "watch"]}, {"apiGroups": ["argoproj.io"], "resources": ["workflows"], "verbs": ["get"]}, {"apiGroups": ["argoproj.io"], "resources": ["workflowtaskresults"], "verbs": ["create", "patch"]}]), obj("RoleBinding", "modeldev-step", TENANT, roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "modeldev-step"}, subjects=[{"kind": "ServiceAccount", "name": "modeldev-step", "namespace": TENANT}]), obj("ConfigMap", name, SYSTEM, data={"fixture.json": encoded(fixture).decode(), "facts.json": encoded(facts).decode(), "binding.json": encoded(binding).decode(), "s3-ca.crt": (private / "s3-ca.crt").read_text(), "kfp-ca.crt": (private / "kfp-ca.crt").read_text()}), obj("PersistentVolumeClaim", "core-flow-evidence", SYSTEM, spec={"storageClassName": "ani-cephfs", "accessModes": ["ReadWriteMany"], "resources": {"requests": {"storage": "1Gi"}}})]
    # Reuse the observed KFP 2.16 namespace provider format, with this task's
    # bucket and finite artifact identity. Neither shared configuration nor its
    # credential values are copied into the fixture.
    providers = {"s3": {"default": {"endpoint": "ani-rustfs-svc.ani-platform.svc.cluster.local:9000", "region": "us-east-1", "disableSSL": False, "forcePathStyle": True, "maxRetries": 2, "credentials": {"fromEnv": False, "secretRef": {"secretName": "mlpipeline-minio-artifact", "accessKeyKey": "accesskey", "secretKeyKey": "secretkey"}}}}}
    config_rows.append(obj("ConfigMap", "kfp-launcher", TENANT, data={"clusterDomain": "cluster.local", "defaultPipelineRoot": binding["owner"]["pipeline_root"], "providers": encoded(providers).decode()}))
    step_role = next(row for row in config_rows if row["kind"] == "Role" and row["metadata"]["name"] == "modeldev-step")
    step_role["rules"] += [{"apiGroups": [""], "resources": ["configmaps"], "resourceNames": ["kfp-launcher", "ani-kfp-ca", "metadata-grpc-configmap", "kube-root-ca.crt"], "verbs": ["get"]}, {"apiGroups": [""], "resources": ["secrets"], "resourceNames": ["mlpipeline-minio-artifact", "ani-kfp-ca"], "verbs": ["get"]}]
    volumes = [{"name": "config", "configMap": {"name": name}}, {"name": "materials", "emptyDir": {}}, {"name": "database", "secret": {"secretName": "core-flow-database", "items": [{"key": "database.dsn", "path": "database.dsn"}]}}, {"name": "tls", "secret": {"secretName": "core-flow-modeldev-tls"}}, {"name": "storage", "secret": {"secretName": "core-flow-storage", "items": [{"key": "control.json", "path": "control.json"}]}}, {"name": "pipeline-identity", "projected": {"sources": [{"serviceAccountToken": {"audience": "pipelines.kubeflow.org", "expirationSeconds": 3600, "path": "token"}}]}}, {"name": "catalogue", "emptyDir": {}}, {"name": "tmp", "emptyDir": {"sizeLimit": "256Mi"}}, {"name": "evidence", "persistentVolumeClaim": {"claimName": "core-flow-evidence"}}]
    mounts = [{"name": n, "mountPath": path, "readOnly": read_only} for n, path, read_only in [("config", "/config", True), ("config", "/public", True), ("materials", "/materials", True), ("database", "/database", True), ("tls", "/tls", True), ("storage", "/storage", True), ("pipeline-identity", "/pipeline-identity", True), ("catalogue", "/catalogue", False), ("tmp", "/tmp", False), ("evidence", "/evidence", False)]]
    security = {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}
    init = {"name": "copy-facts", "image": driver_image, "command": ["/opt/venv/bin/python", "-I", "-B", "-c", "from pathlib import Path; p=Path('/materials/facts.json'); p.write_bytes(Path('/config/facts.json').read_bytes()); p.chmod(0o600)"], "volumeMounts": [{"name": "config", "mountPath": "/config", "readOnly": True}, {"name": "materials", "mountPath": "/materials"}], "securityContext": security, "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "500m", "memory": "128Mi"}}}
    container = {"name": "driver", "image": driver_image, "imagePullPolicy": "IfNotPresent", "env": [{"name": "ANI_FINETUNING_CORE_FLOW_FIXTURE", "value": "/config/fixture.json"}], "volumeMounts": mounts, "securityContext": security, "resources": {"requests": {"cpu": "100m", "memory": "256Mi"}, "limits": {"cpu": "2", "memory": "2Gi"}}}
    job = obj("Job", name, SYSTEM, spec={"backoffLimit": 0, "activeDeadlineSeconds": 11160, "template": {"metadata": {"labels": {"app": "core-flow-driver", "ani.io/managed-by": OWNER}, "annotations": {"ani.modeldev/source-commit": source_commit}}, "spec": {"serviceAccountName": "modeldev-control", "restartPolicy": "Never", "securityContext": {"fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}}, "initContainers": [init], "containers": [container], "volumes": volumes}}})
    job["apiVersion"] = "batch/v1"
    for row in [*config_rows, job]:
        cluster.run(["apply", "--dry-run=server", "-f", "-", "-o", "name"], encoded(row))
    manifest = private / (name + "-manifest.json")
    save(manifest, {"apiVersion": "v1", "kind": "List", "items": [*config_rows, job]})
    save(root / "driver-environment.json", proof)
    state["scenarios"][args.scenario] = {"job_name": name, "execution_id": execution, "operation_id": operation, "manifest_reference": str(manifest), "manifest_sha256": sha(manifest.read_bytes()), "source_commit": source_commit, "release_digest": digest, "phase": "RENDERED_SERVER_DRY_RUN"}
    save(state_path, state)
    print(json.dumps({"phase": "CORE_FLOW_DRIVER_RENDERED", "scenario": args.scenario, "execution_id": execution, "manifest_reference": str(manifest), "business_acceptance": "NOT_RUN"}))


def start(cluster, root, scenario):
    state_path = root / ".private/driver-state.json"
    state = json.loads(state_path.read_bytes())
    original = state["scenarios"][scenario]
    if original["phase"] != "RENDERED_SERVER_DRY_RUN":
        raise ValueError("inspect original Job/DB; do not create another execution after uncertain start")
    manifest = Path(original["manifest_reference"])
    if sha(manifest.read_bytes()) != original["manifest_sha256"]:
        raise ValueError("frozen driver material drift")
    rows = json.loads(manifest.read_bytes())["items"]
    for row in rows[:-1]:
        cluster.apply(row)
    if cluster.get("job", original["job_name"], SYSTEM, optional=True):
        raise ValueError("original Job already exists; reconcile it without retrying create")
    original["phase"] = "JOB_CREATE_PENDING"
    save(state_path, state)
    cluster.run(["create", "-f", "-", "-o", "name"], encoded(rows[-1]))
    job = cluster.get("job", original["job_name"], SYSTEM)
    original.update(phase="JOB_CREATED", job_uid=job["metadata"]["uid"])
    save(state_path, state)
    print(json.dumps({"phase": "CORE_FLOW_DRIVER_JOB_CREATED", "scenario": scenario, "execution_id": original["execution_id"], "job_uid": original["job_uid"]}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["pipeline", "render", "start"])
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--pipeline-ir", type=Path)
    parser.add_argument("--release", type=Path)
    parser.add_argument("--source-commit")
    parser.add_argument("--driver-image")
    parser.add_argument("--component-image")
    parser.add_argument("--scenario", choices=["a", "b", "stop", "restart", "deadline"])
    parser.add_argument("--remaining-seconds", type=int, default=0)
    args = parser.parse_args()
    if not Path("/etc/fedora-release").exists():
        raise SystemExit("execute only on Fedora")
    os.umask(0o077)
    cluster = Cluster(args.kubectl, args.kubeconfig)
    guard(cluster)
    if args.command == "pipeline":
        pipeline(cluster, args.root, args.pipeline_ir)
    elif args.command == "render":
        render(cluster, args)
    else:
        start(cluster, args.root, args.scenario)


if __name__ == "__main__":
    main()
