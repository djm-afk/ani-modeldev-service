#!/usr/bin/env python3
"""Generate independent Python golden vectors for the single active contract.

All identities, registries and workloads below are synthetic test fixtures.
The implementation defaults have one source shared with the real Go/Python code.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    arguments = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    destination = root / "contract/cpup01/conformance"
    source = (destination / "fixtures.go").read_text()
    request = re.search(r'const IntentRequest = `([^`]*)`', source).group(1).encode()
    intent = json.loads(request)
    frozen_intent = {"schema": "ani.modeldev.intent.v3", **intent}
    intent_bytes = canonical(frozen_intent)
    definitions = json.loads((root / "contract/cpup01/implementations.json").read_bytes())["implementations"]
    defaults = definitions["builtin.mlp"]["defaults"]
    parameters = [{"name": key, "type": "DECIMAL" if key == "learning_rate" else "INTEGER", "value": str(value)} for key, value in sorted(defaults.items())]
    selection = {"schema_version": 1, "preset_id": intent["compute_preset_id"], "code": "cpu-small", "name": "CPU small", "compute_type": "cpu", "cpu_millis": 1000, "memory_mib": 1024, "gpu": None}
    # The provider has its own published field-order canonical format.
    selection_bytes = json.dumps(selection, ensure_ascii=False, separators=(",", ":")).encode()
    namespace_uid = "33333333-3333-4333-8333-333333333333"
    execution_id = "99999999-9999-4999-8999-999999999999"
    runtime_spec = {"podGroupPolicy": {"volcano": {}}, "template": {"spec": {"replicatedJobs": []}}}
    snapshot = {
        "schema_version": "ani.modeldev.execution-spec.v3", "kind": "GENERAL_TRAINING", "delivery_mode": "SAVE_ARTIFACTS", "limits":{"max_content_bytes":"17179869184"},
        "identity_ref": {"tenant_id": "77777777-7777-4777-8777-777777777777", "actor": "governance:user:contract-test", "execution_id": execution_id, "operation_id": "88888888-8888-4888-8888-888888888888", "intent_hash": digest(intent_bytes)},
        "implementation": {"implementation_id": "builtin.mlp", "version": "1", "parameter_contract_version": "ani.mlp.parameters.v3", "pipeline_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "pipeline_version_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "pipeline_ir_sha256": "b" * 64},
        "compute_origin": {"compute_preset_id": intent["compute_preset_id"], "selection_digest": digest(b"compute-selection-v1\n" + selection_bytes), "selection": selection_bytes.decode(), "cpu_millis": "1000", "memory_mib": "1024", "device_mode": "cpu"},
        "timing": {"accepted_at": "2026-10-22T00:00:00Z", "execution_timeout_seconds": 3600, "execution_timeout_max_seconds": 7200, "step_timeout_seconds": 900, "execution_deadline_at": "2026-10-22T01:00:00Z"},
        "runtime_plan": {"generator_version": "contract-fixture-v1", "api_group": "trainer.kubeflow.org", "kind": "TrainingRuntime", "namespace": "cpu-contract-fixture", "name": "mdrt-" + digest((namespace_uid + "\n" + execution_id).encode())[:48], "expected_spec": runtime_spec, "expected_spec_sha256": digest(canonical(runtime_spec)), "defaulting_rules": "trainer-v2.1-explicit-v1", "target_jobs": ["node"]},
        "input": {"input_version_id": intent["dataset_version_id"], "object": {"storage_connection_id": "input-store-v1", "bucket": "cpu-inputs", "key": "fixed/input/data.csv", "version_id": "input-version-1", "size_bytes": "192456", "sha256": "d" * 64}, "format": "CSV", "schema_version": "ani.cpu.csv.v1", "row_count": "1027", "feature_count": 16, "prepared_dataset": {"format":"CPU_CSV","transform_id":"csv-identity-v3","size_bytes":"192456","sha256":"d"*64,"row_count":"1027"}},
        "program": {"image_id":"img_ffffffffffffffffffffffffffffffff","image_version":"1","image_scope":"platform", "image_digest": "registry.example.test/cpu/mlp@sha256:" + "e" * 64, "command": ["python", "/opt/mlp/train.py"], "resolved_args": ["--data", "/inputs/data.csv", "--output", "/outputs"], "resolved_parameters": parameters},
        "training_image": {"registry_authority":"registry.example.test","platform":{"os":"linux","architecture":"amd64"},"protocol_version":"ani.modeldev.training-image-protocol.v1","pull_secret_ref":"ani-pull-" + "a" * 32},
        "resources": {"nodes": 1, "processes_per_node": 1, "request_millicpu": "1000", "limit_millicpu": "1000", "request_memory_bytes": "1073741824", "limit_memory_bytes": "1073741824"},
        "environment": {"binding_id": "11111111-1111-4111-8111-111111111111", "binding_digest": "f" * 64, "cluster_id": "22222222-2222-4222-8222-222222222222", "namespace_name": "cpu-contract-fixture", "namespace_uid": namespace_uid, "kfp_connection_ref": "kfp-managed-v1", "experiment_id": "44444444-4444-4444-8444-444444444444", "identities": {"modeldev_control_identity_ref": "modeldev-control-v1", "kfp_step_service_account": "cpu-managed-step", "trainer_service_account": "cpu-training", "verifier_service_account": "cpu-verifier", "tenant_proxy_identity": "tenant:contract-fixture"}},
        "workspace": {"mode": "EXECUTION_PVC", "storage_class": "task-workspace", "capacity_bytes": "2147483648", "input_subpath": "inputs", "training_subpath": "training", "reports_subpath": "reports", "publication_subpath": "publication", "work_subpath": "work"},
        "publication_scope": {"storage_connection_id": "artifact-store-v1", "bucket": "cpu-artifacts", "approved_prefix": "tenant-fixed/executions", "credential_reference": "modeldev-publish-v1"},
        "output_contract": {"schema_version": "ani.modeldev.output-contract.v3", "output_kind": "CHECKPOINT", "delivery_mode": "SAVE_ARTIFACTS", "required_files": sorted([{"role": "SUMMARY", "relative_path": "summary.json", "in_tar": True}, {"role": "CHECKPOINT", "relative_path": "model.pt", "in_tar": True}, {"role": "MODEL_CONFIG", "relative_path": "model_config.json", "in_tar": True}, {"role": "METRICS", "relative_path": "metrics.jsonl", "in_tar": True}], key=lambda item: item["relative_path"]), "create_tar_bundle": True},
        "deadline_at": "2026-10-22T01:00:00Z",
    }
    dataset = {"schema_version":"ani.modeldev.dataset-manifest.v3","input_version_id":intent["dataset_version_id"],"source_format":"CSV","source_size_bytes":"192456","source_sha256":"d"*64,"row_count":"1027","prepared_format":"CPU_CSV","transform_id":"csv-identity-v3","prepared_relative_path":"inputs/data.csv","prepared_size_bytes":"192456","prepared_sha256":"d"*64}
    snapshot["program"]["resolved_args"]=["--dataset-manifest","/inputs/dataset-manifest.json","--expected-dataset-manifest-sha256",digest(canonical(dataset)),"--work-root","/workspace/work/train","--output","/outputs"]
    snapshot_bytes = canonical(snapshot)
    outputs = {"intent-input-v3.json": request, "intent-canonical-v3.json": intent_bytes, "snapshot-v3.json": snapshot_bytes}
    outputs["vectors.go"] = ("// Code generated by scripts/generate-contract-vectors.py; DO NOT EDIT.\npackage conformance\n\nconst IntentSHA256V1 = \"" + digest(intent_bytes) + "\"\nconst SnapshotSHA256V1 = \"" + digest(snapshot_bytes) + "\"\n").encode()
    for name, data in outputs.items():
        target = destination / name
        if arguments.check:
            if not target.is_file() or target.read_bytes() != data:
                raise ValueError("generated fixture differs: " + name)
        else:
            target.write_bytes(data)
        print(digest(data) + "  " + name)


if __name__ == "__main__":
    main()
