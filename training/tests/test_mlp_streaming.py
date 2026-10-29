"""Current manifest and bounded MLP behavior; execute on Fedora only."""

import csv
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from dataset_fixture import manifest_arguments

TRAINING_ROOT = Path(__file__).resolve().parents[1]


class StreamingMLPTest(unittest.TestCase):
    def train(self, root, *extra):
        return subprocess.run(
            [sys.executable, "-I", str(TRAINING_ROOT / "train_mlp.py"),
             *manifest_arguments(root / "inputs/data.csv"),
             "--output", str(root / "outputs"), *map(str, extra)],
            capture_output=True, text=True, timeout=120,
            env={**os.environ, "WORLD_SIZE": "1", "CUDA_VISIBLE_DEVICES": ""},
        )

    def test_current_manifest_trains_all_tail_samples_and_preserves_identities(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            with data.open("w", newline="") as stream:
                writer = csv.writer(stream)
                writer.writerow([f"x{i}" for i in range(16)] + ["label"])
                writer.writerows([[0] * 16 + [i % 2] for i in range(17)])
            checksum = hashlib.sha256(data.read_bytes()).hexdigest()
            trained = self.train(root, "--epochs", "2", "--batch-size", "6")
            self.assertEqual(trained.returncode, 0, trained.stderr)
            summary = json.loads((root / "outputs/summary.json").read_bytes())
            self.assertEqual((summary["source_samples"], summary["samples"], summary["steps"]),
                             (17, 17, 6))
            self.assertEqual(summary["input_sha256"], checksum)
            self.assertEqual(summary["prepared_input_sha256"], checksum)
            self.assertEqual(summary["input_version_id"], "00000000-0000-4000-8000-000000000003")
            self.assertEqual(summary["dataset_manifest_sha256"],
                             hashlib.sha256((root / "inputs/dataset-manifest.json").read_bytes()).hexdigest())
            self.assertEqual(hashlib.sha256(data.read_bytes()).hexdigest(), checksum)
            # An independent full CPU forward is small here and checks that the
            # five-sample tail gets its sample weight rather than batch weight.
            import torch
            from torch import nn
            model = nn.Sequential(nn.Linear(16, 32), nn.ReLU(), nn.Linear(32, 2))
            model.load_state_dict(torch.load(root / "outputs/model.pt", weights_only=True))
            with torch.no_grad():
                reference = nn.functional.cross_entropy(
                    model(torch.zeros(17, 16)), torch.tensor([i % 2 for i in range(17)]),
                    reduction="sum",
                ).item() / 17
            self.assertAlmostEqual(summary["final_loss"], reference, places=6)
            candidate = json.loads((root / "outputs/result.json").read_bytes())
            metrics = (root / "outputs/metrics.jsonl").read_bytes()
            metric_ref = next(item for item in candidate["files"] if item["path"] == "metrics.jsonl")
            self.assertEqual((metric_ref["bytes"], metric_ref["sha256"]),
                             (str(len(metrics)), hashlib.sha256(metrics).hexdigest()))

    def test_frozen_manifest_and_prepared_file_tampering_are_rejected(self):
        cases = ("manifest_digest", "prepared_sha", "row_count", "old_schema", "path_escape",
                 "unknown_field", "numeric_row_count", "mmap_overflow", "quoted_newline",
                 "numeric_underscore", "numeric_unicode")
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                data = root / "inputs/data.csv"
                data.parent.mkdir()
                with data.open("w", newline="") as stream:
                    writer = csv.writer(stream)
                    writer.writerow([f"x{i}" for i in range(16)] + ["label"])
                    first = {"quoted_newline": "0\n0", "numeric_underscore": "1_0",
                             "numeric_unicode": "١"}.get(case, "0")
                    writer.writerow([first] + [0] * 15 + [1])
                arguments = list(manifest_arguments(data))
                path = Path(arguments[1])
                manifest = json.loads(path.read_bytes())
                if case == "manifest_digest":
                    arguments[-1] = "0" * 64
                elif case == "prepared_sha":
                    data.write_bytes(data.read_bytes().replace(b"label", b"LABEL"))
                elif case == "row_count":
                    manifest["row_count"] = "2"
                elif case == "old_schema":
                    manifest["schema_version"] = "ani.modeldev.dataset-manifest.v2"
                elif case == "path_escape":
                    manifest["prepared_relative_path"] = "inputs/../elsewhere.csv"
                elif case == "unknown_field":
                    manifest["expected_input_bytes"] = "1"
                elif case == "numeric_row_count":
                    manifest["row_count"] = 1
                elif case == "mmap_overflow":
                    manifest["row_count"] = "9223372036854775807"
                if case != "manifest_digest":
                    raw = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
                    path.write_bytes(raw)
                    arguments[-1] = hashlib.sha256(raw).hexdigest()
                result = subprocess.run(
                    [sys.executable, "-I", str(TRAINING_ROOT / "train_mlp.py"),
                     *arguments, "--output", str(root / "outputs")],
                    capture_output=True, text=True, timeout=120,
                )
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('"train.loss"', result.stdout)
                self.assertFalse((root / "outputs").exists(), result.stderr)

    def test_legacy_input_arguments_are_rejected(self):
        result = subprocess.run(
            [sys.executable, "-I", str(TRAINING_ROOT / "train_mlp.py"),
             "--data", "old.csv", "--expected-input-bytes", "1",
             "--expected-input-sha256", "0" * 64, "--output", "old-output"],
            capture_output=True, text=True, timeout=120,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--dataset-manifest", result.stderr)

    def test_csv_above_32_mib_with_large_numeric_fields_trains_without_truncation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = root / "inputs/data.csv"
            data.parent.mkdir()
            with data.open("wb") as stream:
                stream.write((",".join([f"x{i}" for i in range(16)] + ["label"]) + "\n").encode())
                for index in range(33):
                    stream.write(b"0" * (1024 * 1024))
                    stream.write((",0" * 15 + f",{index % 2}\n").encode())
            self.assertGreater(data.stat().st_size, 32 * 1024 * 1024)
            result = self.train(root, "--epochs", "1", "--batch-size", "16")
            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads((root / "outputs/summary.json").read_bytes())
            self.assertEqual((summary["samples"], summary["steps"]), (33, 3))
            self.assertEqual(summary["input_bytes"], str(data.stat().st_size))

    def test_logical_csv_record_boundary_counts_quotes_and_crlf(self):
        limits = json.loads((TRAINING_ROOT / "structure_limits.json").read_bytes())
        maximum = limits["MaxRecordBytes"]
        tail = b'"' + b",0" * 15 + b",1\r\n"
        for extra in (0, 1):
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                data = root / "inputs/data.csv"
                data.parent.mkdir()
                with data.open("wb") as stream:
                    stream.write((",".join([f"x{i}" for i in range(16)] + ["label"]) + "\r\n").encode())
                    stream.write(b'"')
                    stream.write(b"0" * (maximum - 1 - len(tail) + extra))
                    stream.write(tail)
                result = self.train(root, "--epochs", "1", "--batch-size", "1")
                if extra == 0:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    summary = json.loads((root / "outputs/summary.json").read_bytes())
                    self.assertEqual((summary["samples"], summary["steps"]), (1, 1))
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(f"actual={maximum + 1} limit={maximum}", result.stderr)
                    self.assertFalse((root / "outputs").exists())


if __name__ == "__main__":
    unittest.main()
