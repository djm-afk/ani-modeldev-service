"""Frozen dataset identity consumed by both real training programs."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location(
    "current_dataset_manifest", Path(__file__).resolve().parents[1] / "dataset_manifest.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class DatasetManifestTest(unittest.TestCase):
    def fixture(self, root):
        data = root / "inputs/data/finetune_data.jsonl"
        data.parent.mkdir(parents=True)
        raw = b'{"instruction":"a","input":"","output":"b"}\n'
        data.write_bytes(raw)
        manifest = {
            "schema_version": "ani.modeldev.dataset-manifest.v3",
            "input_version_id": "00000000-0000-4000-8000-000000000003",
            "source_format": "JSON", "source_size_bytes": str(len(raw) + 2),
            "source_sha256": "a" * 64, "row_count": "1",
            "prepared_format": "ALPACA_JSONL", "transform_id": "alpaca-array-compact-jsonl-v3",
            "prepared_relative_path": "inputs/data/finetune_data.jsonl",
            "prepared_size_bytes": str(len(raw)), "prepared_sha256": hashlib.sha256(raw).hexdigest(),
        }
        return data, manifest

    def write_manifest(self, root, manifest):
        path = root / "inputs/dataset-manifest.json"
        raw = json.dumps(manifest, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        path.write_bytes(raw)
        return path, hashlib.sha256(raw).hexdigest()

    def test_frozen_jsonl_is_stream_verified_and_metadata_does_not_open_dataset(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            data, manifest = self.fixture(root)
            path, sha = self.write_manifest(root, manifest)
            actual = MODULE.load_dataset_manifest(path, sha, "JSON")
            MODULE.verify_prepared_dataset(root, actual)
            data.unlink()
            self.assertEqual(MODULE.load_dataset_manifest(path, sha, "JSON"), actual)
            with self.assertRaises(FileNotFoundError):
                MODULE.verify_prepared_dataset(root, actual)

    def test_current_manifest_rejects_wrong_identity_representation_and_path(self):
        cases = {
            "schema_version": "ani.modeldev.dataset-manifest.v2",
            "input_version_id": "00000000-0000-0000-0000-000000000000",
            "row_count": 1, "source_size_bytes": "01", "prepared_sha256": "A" * 64,
            "prepared_relative_path": "inputs/data/../finetune_data.jsonl",
            "prepared_format": "ALPACA_JSON", "transform_id": "v2",
        }
        for key, value in cases.items():
            with self.subTest(key=key), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                _, manifest = self.fixture(root)
                manifest[key] = value
                path, sha = self.write_manifest(root, manifest)
                with self.assertRaises(ValueError):
                    MODULE.load_dataset_manifest(path, sha, "JSON")

    def test_size_sha_row_and_final_lf_are_all_verified(self):
        for case in ("bytes", "sha", "rows", "lf"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                data, manifest = self.fixture(root)
                if case == "bytes":
                    data.write_bytes(data.read_bytes() + b" ")
                elif case == "sha":
                    manifest["prepared_sha256"] = "f" * 64
                elif case == "rows":
                    manifest["row_count"] = "2"
                else:
                    data.write_bytes(data.read_bytes()[:-1] + b" ")
                    manifest["prepared_sha256"] = hashlib.sha256(data.read_bytes()).hexdigest()
                with self.assertRaises(ValueError):
                    MODULE.verify_prepared_dataset(root, manifest)


if __name__ == "__main__":
    unittest.main()
