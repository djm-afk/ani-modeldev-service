"""Current prepared CSV fixture for the training CLI behavior tests."""

import csv
import hashlib
import json
from pathlib import Path


def manifest_arguments(data, *, expected_sha256=None, expected_bytes=None):
    limits = json.loads((Path(__file__).resolve().parents[1]
                        / "structure_limits.json").read_bytes())
    csv.field_size_limit(limits["MaxRecordBytes"])
    digest = hashlib.sha256()
    size = 0
    with data.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
            size += len(chunk)
    with data.open(newline="", encoding="utf-8") as stream:
        rows = sum(1 for _ in csv.reader(stream)) - 1
    checksum = expected_sha256 if expected_sha256 is not None else digest.hexdigest()
    size = expected_bytes if expected_bytes is not None else size
    manifest = {
        "schema_version": "ani.modeldev.dataset-manifest.v3",
        "input_version_id": "00000000-0000-4000-8000-000000000003",
        "source_format": "CSV",
        "source_size_bytes": str(size),
        "source_sha256": checksum,
        "row_count": str(rows),
        "prepared_format": "CPU_CSV",
        "transform_id": "csv-identity-v3",
        "prepared_relative_path": "inputs/data.csv",
        "prepared_size_bytes": str(size),
        "prepared_sha256": checksum,
    }
    path = data.with_name("dataset-manifest.json")
    raw = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
    path.write_bytes(raw)
    return (
        "--dataset-manifest", str(path),
        "--work-root", str(data.parent.parent / "work/train"),
        "--expected-dataset-manifest-sha256", hashlib.sha256(raw).hexdigest(),
    )
