#!/usr/bin/env python3
"""Render the Dataset importer material for an existing tenant Namespace.

The importer ServiceAccount and its artifact-store CA are Dataset material, not
part of the training binding: they must exist for a tenant that has a standard
Namespace but no training execution-registry row (design §7.3, §12.4). The
platform installer applies this manifest; the ModelDev worker only verifies it.
"""
import argparse
import json
import re
from pathlib import Path

UUID = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
ZERO = "00000000-0000-0000-0000-000000000000"
IMPORTER = "modeldev-dataset-importer"
IMPORT_CA = "modeldev-dataset-import-ca"
PURPOSE = "modeldev.ani.io/purpose"
PURPOSE_IMPORT = "dataset-import"


def identity(tenant_id, namespace_uid):
    if not isinstance(tenant_id, str) or not re.fullmatch(UUID, tenant_id) or tenant_id == ZERO:
        raise ValueError("canonical tenant UUID required")
    if not isinstance(namespace_uid, str) or not re.fullmatch(UUID, namespace_uid) or namespace_uid == ZERO:
        raise ValueError("canonical Namespace UID required")
    return {
        "labels": {
            "ani.io/managed-by": "ani-modeldev-service",
            PURPOSE: PURPOSE_IMPORT,
            "modeldev.ani.io/tenant-id": tenant_id,
        },
        "annotations": {"modeldev.ani.io/namespace-uid": namespace_uid},
    }


def render(tenant_id, namespace_uid, s3_ca):
    if not isinstance(s3_ca, str) or not s3_ca.strip():
        raise ValueError("artifact store CA required")
    meta = identity(tenant_id, namespace_uid)
    namespace = "tenant-" + tenant_id
    # These shapes must stay identical to datasetImporterServiceAccount and
    # datasetImporterCA in internal/data/tenantenvironment/kubernetes.go.
    return {"apiVersion": "v1", "kind": "List", "items": [
        {
            "apiVersion": "v1", "kind": "ServiceAccount",
            "metadata": dict(meta, name=IMPORTER, namespace=namespace),
            "automountServiceAccountToken": False,
            "imagePullSecrets": [],
        },
        {
            "apiVersion": "v1", "kind": "ConfigMap",
            "metadata": dict(meta, name=IMPORT_CA, namespace=namespace),
            "immutable": True,
            "data": {"s3-ca.pem": s3_ca},
        },
    ]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tenant-id", required=True)
    parser.add_argument("--namespace-uid", required=True)
    parser.add_argument("--s3-ca-file", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    document = render(args.tenant_id, args.namespace_uid, args.s3_ca_file.read_text())
    args.output.mkdir(parents=True, exist_ok=False)
    (args.output / "dataset-material.json").write_text(json.dumps(document, indent=2) + "\n")


if __name__ == "__main__":
    main()
