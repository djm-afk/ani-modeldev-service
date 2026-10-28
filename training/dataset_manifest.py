"""Strict current dataset identities; no dataset is materialized in memory."""

import hashlib
import json
import os
from pathlib import Path
import re
import stat
import uuid


STRUCTURE_LIMITS = json.loads(Path(__file__).with_name("structure_limits.json").read_bytes())
MAX_MANIFEST_BYTES = STRUCTURE_LIMITS["MaxManifestBytes"]
MAX_RECORD_BYTES = STRUCTURE_LIMITS["MaxRecordBytes"]
INT64_MAX = 9223372036854775807


def unique_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate manifest field: {key}")
        result[key] = value
    return result


def regular_file(path):
    path = Path(os.path.abspath(path))
    for component in (*reversed(path.parents), path):
        if component.is_symlink():
            raise ValueError("symbolic links are forbidden in dataset paths")
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    stream = os.fdopen(descriptor, "rb")
    if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
        stream.close()
        raise ValueError("dataset inputs must be regular files")
    return stream


def positive_int64(value):
    if (not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]*", value)
            or len(value) > 19 or int(value) > INT64_MAX):
        raise ValueError("dataset counts and sizes must be positive int64 decimal strings")
    return int(value)


def load_dataset_manifest(path, expected_sha256, source_format):
    path = Path(path)
    if not isinstance(expected_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_sha256):
        raise ValueError("expected dataset manifest SHA256 must be lowercase hexadecimal")
    if path.name != "dataset-manifest.json" or path.parent.name != "inputs":
        raise ValueError("dataset manifest must be inputs/dataset-manifest.json")
    with regular_file(path) as stream:
        raw = stream.read(MAX_MANIFEST_BYTES + 1)
    if len(raw) > MAX_MANIFEST_BYTES:
        raise ValueError(f"MaxManifestBytes exceeded: actual={len(raw)} limit={MAX_MANIFEST_BYTES}")
    if hashlib.sha256(raw).hexdigest() != expected_sha256:
        raise ValueError("dataset manifest does not match the frozen SHA256")
    manifest = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_fields)
    fields = {
        "schema_version", "input_version_id", "source_format", "source_size_bytes",
        "source_sha256", "row_count", "prepared_format", "transform_id",
        "prepared_relative_path", "prepared_size_bytes", "prepared_sha256",
    }
    if not isinstance(manifest, dict) or set(manifest) != fields or any(type(v) is not str for v in manifest.values()):
        raise ValueError("dataset manifest requires exactly the current string fields")
    if manifest["schema_version"] != "ani.modeldev.dataset-manifest.v3" or manifest["source_format"] != source_format:
        raise ValueError("dataset manifest schema or source format differs")
    try:
        identity = uuid.UUID(manifest["input_version_id"])
    except ValueError as error:
        raise ValueError("dataset input version must be a UUID") from error
    if identity.int == 0 or str(identity) != manifest["input_version_id"]:
        raise ValueError("dataset input version must be a canonical nonzero UUID")
    for key in ("source_sha256", "prepared_sha256"):
        if not re.fullmatch(r"[0-9a-f]{64}", manifest[key]):
            raise ValueError(f"invalid dataset {key}")
    for key in ("source_size_bytes", "prepared_size_bytes", "row_count"):
        positive_int64(manifest[key])
    if source_format == "CSV":
        expected = ("CPU_CSV", "csv-identity-v3", "inputs/data.csv")
        if (manifest["source_size_bytes"] != manifest["prepared_size_bytes"]
                or manifest["source_sha256"] != manifest["prepared_sha256"]):
            raise ValueError("CSV identity differs from the frozen source")
    elif source_format == "JSON":
        expected = ("ALPACA_JSONL", "alpaca-array-compact-jsonl-v3", "inputs/data/finetune_data.jsonl")
        if int(manifest["prepared_size_bytes"]) > int(manifest["source_size_bytes"]):
            raise ValueError("derived JSONL exceeds its frozen source")
    else:
        raise ValueError("unknown dataset source format")
    if tuple(manifest[k] for k in ("prepared_format", "transform_id", "prepared_relative_path")) != expected:
        raise ValueError("dataset manifest prepared path or transform differs")
    canonical = json.dumps(manifest, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    if raw != canonical:
        raise ValueError("dataset manifest must use current canonical JSON bytes")
    return manifest


def verify_prepared_dataset(workspace, manifest):
    """Hash/count exact prepared bytes in bounded blocks, never parse an array."""
    expected_size = positive_int64(manifest["prepared_size_bytes"])
    checksum, size, rows, last = hashlib.sha256(), 0, 0, b""
    with regular_file(Path(workspace) / manifest["prepared_relative_path"]) as stream:
        before = os.fstat(stream.fileno())
        if before.st_size != expected_size:
            raise ValueError("prepared dataset size differs from frozen identity")
        while block := stream.read(min(1024 * 1024, expected_size - size + 1)):
            size += len(block)
            if size > expected_size:
                raise ValueError("prepared dataset grew while being read")
            checksum.update(block)
            rows += block.count(b"\n")
            last = block[-1:]
        after = os.fstat(stream.fileno())
    if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (
            after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns):
        raise ValueError("prepared dataset changed while being read")
    if size != expected_size or checksum.hexdigest() != manifest["prepared_sha256"]:
        raise ValueError("prepared dataset does not match frozen bytes/SHA256")
    if manifest["prepared_format"] == "ALPACA_JSONL" and (
            last != b"\n" or rows != positive_int64(manifest["row_count"])):
        raise ValueError("prepared JSONL row count or final LF differs")
