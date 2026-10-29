"""Deterministic A12 source fixture behavior; execute on Fedora only."""

import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

GENERATOR = Path(__file__).resolve().parents[2] / "scripts/testing/make_long_alpaca.py"


class LongAlpacaFixtureTest(unittest.TestCase):
    def test_one_record_has_exact_object_bytes_and_actual_reproducible_facts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            digests = []
            for index in range(2):
                source = root / f"data-{index}.json"
                facts = source.with_suffix(".facts.json")
                generated = subprocess.run(
                    [sys.executable, "-I", str(GENERATOR), "--records", "1",
                     "--output", str(source), "--facts", str(facts)],
                    capture_output=True, text=True, timeout=120,
                )
                self.assertEqual(generated.returncode, 0, generated.stderr)
                raw = source.read_bytes()
                self.assertEqual(len(raw), 31457282)
                self.assertEqual(len(raw[1:-1]), 31457280)
                row = json.loads(raw)[0]
                self.assertEqual(set(row), {"instruction", "input", "output"})
                self.assertTrue(row["instruction"].startswith("Record 00000001. "))
                self.assertEqual(row["output"], "Record 00000001: complete.")
                self.assertGreaterEqual(sum(len(value.encode()) for value in row.values()), 29 * 1024 * 1024)
                actual_sha = hashlib.sha256(raw).hexdigest()
                recorded = json.loads(facts.read_bytes())
                self.assertEqual((recorded["row_count"], recorded["source_size_bytes"], recorded["source_sha256"]),
                                 ("1", "31457282", actual_sha))
                self.assertEqual(recorded["records"][0]["object_sha256"], hashlib.sha256(raw[1:-1]).hexdigest())
                digests.append(actual_sha)
            self.assertEqual(digests[0], digests[1])


if __name__ == "__main__":
    unittest.main()
