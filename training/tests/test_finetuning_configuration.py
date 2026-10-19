"""Run on Fedora; verify the frozen parameter boundary without training doubles."""

import importlib.util
import json
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location(
    "finetuning_configuration",
    Path(__file__).parents[1] / "finetuning/configuration.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FrozenConfigurationTests(unittest.TestCase):
    def test_baseline_and_nondefault_roundtrip(self):
        for rate, steps in (("0.0001", 20), ("0.0002", 10)):
            expected = MODULE.projection(rate, steps)
            self.assertEqual(
                MODULE.load_configuration(MODULE.canonical(expected)), expected
            )
            self.assertEqual(
                MODULE.digest(expected),
                MODULE.digest(MODULE.load_configuration(MODULE.canonical(expected))),
            )

    def test_rejects_unapproved_managed_overrides_and_unknown_fields(self):
        for key, value in (
            ("lora_rank", 16),
            ("train_dtype", "bfloat16"),
            ("fp16", True),
            ("seed", True),
        ):
            changed = MODULE.projection("0.0001", 20)
            changed["managed_settings"][key] = value
            with self.assertRaises(ValueError):
                MODULE.load_configuration(json.dumps(changed))
        changed = MODULE.projection("0.0001", 20)
        changed["yaml"] = "arbitrary config"
        with self.assertRaises(ValueError):
            MODULE.load_configuration(json.dumps(changed))

    def test_decimal_integer_and_codec_boundaries(self):
        for rate, steps in (
            ("1e-4", "20"),
            ("0.00010", "20"),
            ("0", "20"),
            ("1.01", "20"),
            ("0.0001", "0"),
            ("0.0001", "201"),
            ("0.0001", "20.0"),
            ("0.0001", 20),
        ):
            changed = MODULE.projection("0.0001", 20)
            changed["parameters"][0]["value"] = rate
            changed["parameters"][1]["value"] = steps
            with self.assertRaises(ValueError):
                MODULE.load_configuration(json.dumps(changed))
        for rate, steps in (("0.00000001", 1), ("1", 200)):
            expected = MODULE.projection(rate, steps)
            self.assertEqual(
                MODULE.load_configuration(MODULE.canonical(expected)), expected
            )

    def test_duplicate_keys_are_rejected(self):
        value = MODULE.canonical(MODULE.projection("0.0001", 20)).decode()
        changed = value.replace('"max_steps"', '"max_steps","name":"max_steps"')
        with self.assertRaises(ValueError):
            MODULE.load_configuration(changed)


if __name__ == "__main__":
    unittest.main()
