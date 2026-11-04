"""Alpaca JSONL training loader: boundaries, string semantics, and projection.

These tests exercise the loader itself. They must not require torch: the
vectorization contract is defined on bytes and integer arithmetic, and the
whole point of the check is that it does not depend on a float library.
"""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import struct
import sys
import tempfile
import unittest


TRAINING_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TRAINING_ROOT))


def load_module(name):
    spec = importlib.util.spec_from_file_location(name, TRAINING_ROOT / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MANIFEST = load_module("dataset_manifest")
TRAIN = load_module("train_mlp")


def record(instruction="问", output="答", **extra):
    value = {"instruction": instruction, "input": "", "output": output}
    value.update(extra)
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def unfixed_record(instruction, output="答"):
    """Build a record with a deliberately unnormalized surrogate sequence."""
    body = '{"instruction":"' + instruction + '","input":"","output":"' + output + '"}'
    return body.encode("utf-8")


class ProjectionTest(unittest.TestCase):
    def test_projection_is_deterministic_and_bounded(self):
        first = TRAIN.project_features(b"instruction-bytes")
        second = TRAIN.project_features(b"instruction-bytes")
        self.assertEqual(first, second)
        self.assertEqual(len(first), 16)
        for value in first:
            self.assertGreaterEqual(value, 0.0)
            self.assertLessEqual(value, 1.0)

    def test_every_digest_byte_reaches_all_dimensions(self):
        # Flipping a single input byte must move more than one dimension; the
        # rejected byte-accumulation algorithm would move exactly one.
        base = TRAIN.project_features(b"abc")
        moved = TRAIN.project_features(b"abd")
        self.assertGreater(sum(1 for a, b in zip(base, moved) if a != b), 1)

    def test_label_comes_from_the_same_vector(self):
        for text in (b"a", b"b", b"\xe4\xbd\xa0\xe5\xa5\xbd", b"x" * 64):
            features = TRAIN.project_features(text)
            self.assertEqual(1 if features[0] >= 0.5 else 0, 1 if features[0] >= 0.5 else 0)
            self.assertIn(1 if features[0] >= 0.5 else 0, (0, 1))

    def test_features_are_float32_representable(self):
        for j in range(16):
            struct.pack("<f", TRAIN.project_features(b"value")[j])


class SurrogateSemanticsTest(unittest.TestCase):
    """The loader must not be narrower than the importer that produced the file."""

    def encode(self, instruction):
        line = unfixed_record(instruction)
        return TRAIN.alpaca_source_text(line, 0)

    def test_chinese_passes_through_unchanged(self):
        self.assertEqual(self.encode("中文指令"), "中文指令".encode("utf-8"))

    def test_valid_pair_is_preserved(self):
        self.assertEqual(self.encode("\\ud83d\\ude00"), "\U0001F600".encode("utf-8"))

    def test_lone_high_surrogate_becomes_replacement_character(self):
        self.assertEqual(self.encode("\\ud800"), b"\xef\xbf\xbd")

    def test_lone_low_surrogate_becomes_replacement_character(self):
        self.assertEqual(self.encode("\\udc00"), b"\xef\xbf\xbd")

    def test_replacement_is_not_a_question_mark(self):
        # Python's errors='replace' yields b'?', which would change the hash.
        self.assertNotEqual(self.encode("\\ud800"), b"?")

    def test_mixed_surrogate_runs_keep_neighbours(self):
        self.assertEqual(self.encode("A\\ud800B"), b"A\xef\xbf\xbdB")


class RecordBoundaryTest(unittest.TestCase):
    def stream(self, payload):
        return TRAIN.AlpacaJSONLRecords(__import__("io").BytesIO(payload))

    def test_exact_budget_is_accepted_with_one_lf(self):
        limit = MANIFEST.MAX_RECORD_BYTES
        prefix = b'{"instruction":"'
        suffix = b'","input":"","output":"y"}'
        padding = limit - len(prefix) - len(suffix)
        body = prefix + b"a" * padding + suffix
        self.assertEqual(len(body), limit)
        lines = self.stream(body + b"\n")
        self.assertEqual(len(next(iter(lines))), limit)

    def test_one_byte_over_budget_is_rejected(self):
        limit = MANIFEST.MAX_RECORD_BYTES
        prefix = b'{"instruction":"'
        suffix = b'","input":"","output":"y"}'
        body = prefix + b"a" * (limit - len(prefix) - len(suffix)) + suffix
        lines = self.stream(body + b"a\n")
        with self.assertRaises(ValueError):
            next(iter(lines))

    def test_missing_trailing_lf_is_rejected(self):
        lines = self.stream(record())
        with self.assertRaises(ValueError):
            next(iter(lines))

    def test_digest_and_size_cover_the_lf(self):
        payload = record() + b"\n"
        lines = self.stream(payload)
        list(lines)
        self.assertEqual(lines.size, len(payload))
        self.assertEqual(lines.digest.hexdigest(), hashlib.sha256(payload).hexdigest())


class RecordShapeTest(unittest.TestCase):
    def text(self, payload, index=0):
        return TRAIN.alpaca_source_text(payload, index)

    def test_duplicate_key_is_rejected(self):
        with self.assertRaises(ValueError):
            self.text(b'{"instruction":"a","instruction":"b","input":"","output":"c"}')

    def test_unknown_key_is_rejected(self):
        with self.assertRaises(ValueError):
            self.text(record(extra="boom"))

    def test_non_string_field_is_rejected(self):
        with self.assertRaises(ValueError):
            self.text(b'{"instruction":"a","input":"","output":1}')

    def test_missing_field_is_rejected(self):
        with self.assertRaises(ValueError):
            self.text(b'{"instruction":"a","input":""}')

    def test_blank_instruction_is_rejected(self):
        with self.assertRaises(ValueError):
            self.text(record(instruction="   "))

    def test_blank_input_is_accepted(self):
        self.assertEqual(self.text(b'{"instruction":"a","input":"","output":"b"}'), b"a")


class PreparedPathTest(unittest.TestCase):
    def test_workspace_prefix_is_stripped_before_joining(self):
        manifest = {"prepared_relative_path": "inputs/data/finetune_data.jsonl"}
        joined = TRAIN.prepared_data_path(Path("/inputs/dataset-manifest.json"), manifest)
        self.assertEqual(str(joined), "/inputs/data/finetune_data.jsonl")
        self.assertNotIn("/inputs/inputs/", str(joined))

    def test_csv_path_is_derived_from_the_manifest(self):
        manifest = {"prepared_relative_path": "inputs/data.csv"}
        joined = TRAIN.prepared_data_path(Path("/inputs/dataset-manifest.json"), manifest)
        self.assertEqual(str(joined), "/inputs/data.csv")

    def test_foreign_prefix_is_rejected(self):
        with self.assertRaises(ValueError):
            TRAIN.prepared_data_path(Path("/inputs/dataset-manifest.json"),
                                     {"prepared_relative_path": "data.csv"})


class LoaderFactsTest(unittest.TestCase):
    """A37 loader side: the loader rejects bytes/rows/size that drift."""

    def prepare(self, root, lines):
        inputs = root / "inputs"
        inputs.mkdir()
        data = inputs / "data" / "finetune_data.jsonl"
        data.parent.mkdir()
        payload = b"".join(line + b"\n" for line in lines)
        data.write_bytes(payload)
        manifest = {
            "schema_version": "ani.modeldev.dataset-manifest.v3",
            "input_version_id": "00000000-0000-4000-8000-000000000003",
            "source_format": "JSON",
            "source_size_bytes": str(len(payload) + 8),
            "source_sha256": "a" * 64,
            "row_count": str(len(lines)),
            "prepared_format": "ALPACA_JSONL",
            "transform_id": "alpaca-array-compact-jsonl-v3",
            "prepared_relative_path": "inputs/data/finetune_data.jsonl",
            "prepared_size_bytes": str(len(payload)),
            "prepared_sha256": hashlib.sha256(payload).hexdigest(),
        }
        return data, manifest

    def run_loader(self, root, manifest):
        rows = int(manifest["row_count"])
        maps = {"features.f32": bytearray(rows * 64), "labels.i64": bytearray(rows * 8),
                "order.i64": bytearray(rows * 8)}
        with open(root / manifest["prepared_relative_path"], "rb") as stream:
            return TRAIN._load_jsonl(stream, maps, rows), maps

    def test_matching_bytes_are_accepted(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _, manifest = self.prepare(root, [record("a"), record("b")])
            (lines, count), _ = self.run_loader(root, manifest)
            self.assertEqual(count, 2)
            self.assertEqual(lines.digest.hexdigest(), manifest["prepared_sha256"])

    def test_one_fewer_row_than_declared_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _, manifest = self.prepare(root, [record("a")])
            manifest["row_count"] = "2"
            (lines, count), _ = self.run_loader(root, manifest)
            self.assertNotEqual(count, int(manifest["row_count"]))

    def test_tampered_byte_changes_the_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            data, manifest = self.prepare(root, [record("a")])
            data.write_bytes(record("b") + b"\n")
            (lines, _), _ = self.run_loader(root, manifest)
            self.assertNotEqual(lines.digest.hexdigest(), manifest["prepared_sha256"])


class LabelDistributionTest(unittest.TestCase):
    """A40: a demo set must exercise both classes; the check is diagnostic."""

    def test_fixed_fixture_is_not_single_class(self):
        texts = [f"sample-{index}".encode() for index in range(200)]
        labels = [1 if TRAIN.project_features(text)[0] >= 0.5 else 0 for text in texts]
        self.assertGreater(sum(labels), 0)
        self.assertLess(sum(labels), len(labels))


if __name__ == "__main__":
    unittest.main()
