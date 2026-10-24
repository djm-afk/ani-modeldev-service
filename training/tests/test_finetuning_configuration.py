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

    def test_all_open_fields_roundtrip_without_combination_whitelist(self):
        expected = MODULE.projection(
            "0.00015",
            30,
            per_device_train_batch_size=2,
            gradient_accumulation_steps=3,
            cutoff_len=96,
            lora_rank=4,
            lora_alpha=7,
            lora_dropout="0.125",
            seed=7,
        )
        self.assertEqual(
            MODULE.load_configuration(MODULE.canonical(expected)), expected
        )
        self.assertEqual(expected["managed_settings"]["data_seed"], 7)
        self.assertEqual(expected["managed_settings"]["lora_alpha"], 7)
        gpu = MODULE.projection(device="cuda")
        self.assertEqual(MODULE.load_configuration(MODULE.canonical(gpu)), gpu)

    def test_decimal_integer_and_codec_boundaries(self):
        for name, raw in (
            ("learning_rate", "1e-4"),
            ("learning_rate", "0.00010"),
            ("learning_rate", "0"),
            ("learning_rate", "9" * 33),
            ("max_steps", "0"),
            ("max_steps", "9223372036854775808"),
            ("max_steps", "20.0"),
            ("max_steps", 20),
            ("seed", "4294967296"),
            ("lora_dropout", "1"),
            ("lora_dropout", "0.99999999999999999999999999999"),
        ):
            changed = MODULE.projection()
            next(p for p in changed["parameters"] if p["name"] == name)["value"] = raw
            with self.subTest(name=name, raw=raw), self.assertRaises(ValueError):
                MODULE.load_configuration(json.dumps(changed))
        for rate, steps in (("0.00000001", 1), ("1000", 201)):
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
