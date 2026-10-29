"""The real SFT entry consumes prepared JSONL, never the source array."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest

SPEC = importlib.util.spec_from_file_location(
    "current_finetune_entry", Path(__file__).resolve().parents[1] / "finetuning/finetune_entry.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FinetuningDatasetTest(unittest.TestCase):
    def test_real_entry_reads_large_jsonl_once_and_export_metadata_without_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = root / "inputs/data/finetune_data.jsonl"
            data.parent.mkdir(parents=True)
            sha = hashlib.sha256()
            size = 0
            with data.open("wb") as stream:
                for _ in range(3):
                    for block in (b'{"instruction":"', b"a" * (12 * 1024 * 1024), b'","input":"","output":"b"}\n'):
                        stream.write(block)
                        sha.update(block)
                        size += len(block)
            manifest = {
                "schema_version": "ani.modeldev.dataset-manifest.v3",
                "input_version_id": "00000000-0000-4000-8000-000000000003",
                "source_format": "JSON", "source_size_bytes": str(size + 2),
                "source_sha256": "a" * 64, "row_count": "3", "prepared_format": "ALPACA_JSONL",
                "transform_id": "alpaca-array-compact-jsonl-v3",
                "prepared_relative_path": "inputs/data/finetune_data.jsonl",
                "prepared_size_bytes": str(size), "prepared_sha256": sha.hexdigest(),
            }
            raw = MODULE.canonical(manifest)
            dataset_manifest = root / "inputs/dataset-manifest.json"
            dataset_manifest.write_bytes(raw)
            configuration = root / "inputs/finetune_configuration.json"
            configuration.write_bytes(MODULE.canonical(MODULE.projection()))
            (root / "inputs/model").mkdir()
            (root / "inputs/model/config.json").write_bytes(b"{}")
            base = MODULE.canonical({"schema": "ani.finetune.base-manifest.v3", "files": MODULE.inventory(root / "inputs/model")})
            (root / "inputs/base_manifest.json").write_bytes(base)
            (root / "inputs/data/dataset_info.json").write_bytes(MODULE.canonical({"ani_cpu_smoke": {
                "file_name": "finetune_data.jsonl", "formatting": "alpaca",
                "columns": {"prompt": "instruction", "query": "input", "response": "output"},
            }}))
            args = SimpleNamespace(workspace=str(root), configuration=str(configuration),
                dataset_manifest=str(dataset_manifest), expected_dataset_manifest_sha256=hashlib.sha256(raw).hexdigest(),
                expected_base_manifest_sha256=hashlib.sha256(base).hexdigest())
            actual_root, actual_configuration, actual_manifest = MODULE.inputs(args)
            self.assertEqual(actual_root, root)
            self.assertEqual(actual_manifest, manifest)
            self.assertEqual(actual_configuration, MODULE.projection())
            data.unlink()
            self.assertEqual(MODULE.inputs(args, verify_data=False)[2], manifest)
            with self.assertRaises(FileNotFoundError):
                MODULE.inputs(args)


if __name__ == "__main__":
    unittest.main()
