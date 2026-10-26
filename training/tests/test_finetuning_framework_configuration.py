"""Run with the locked runtime and ANI_FINETUNE_TEST_BASE pointing at fixed Qwen tokenizer/config files.

The real input/parser/configuration path stops at the external training boundary.
Capability leaves exercise dtype selection; this is not GPU hardware acceptance.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "finetune_entry", Path(__file__).parents[1] / "finetuning/finetune_entry.py"
)
ENTRY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ENTRY)


class ParsedConfigurationObserved(Exception):
    """Stop before model loading, dataset processing, or optimizer updates."""


@unittest.skipUnless(
    os.environ.get("ANI_FINETUNE_TEST_BASE"), "requires the fixed offline Qwen base fixture"
)
class FrameworkConfigurationTests(unittest.TestCase):
    def test_frozen_float32_survives_cpu_and_gpu_capability_selection(self):
        import torch
        from llamafactory.extras import misc
        from llamafactory.train import sft

        base = Path(os.environ["ANI_FINETUNE_TEST_BASE"])
        self.assertEqual(
            hashlib.sha256((base / "config.json").read_bytes()).hexdigest(),
            "18e18afcaccafade98daf13a54092927904649e1dd4eba8299ab717d5d94ff45",
        )
        self.assertFalse(torch.cuda.is_available(), "capability replay must run without a GPU")
        for scenario, bf16, fp16 in (
            ("cpu", False, False),
            ("gpu_bf16_capability", True, True),
            ("gpu_fp16_capability", False, True),
        ):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                workspace = Path(directory)
                shutil.copytree(base, workspace / "inputs/model")
                data = workspace / "inputs/data"
                data.mkdir()
                raw = json.dumps(
                    [{"instruction": "Name one color.", "input": "", "output": "Blue."}] * 64
                ).encode()
                (data / "finetune_data.json").write_bytes(raw)
                ENTRY.write_json(
                    data / "dataset_info.json",
                    {"ani_cpu_smoke": {"file_name": "finetune_data.json", "formatting": "alpaca",
                     "columns": {"prompt": "instruction", "query": "input", "response": "output"}}},
                )
                manifest = workspace / "inputs/base_manifest.json"
                ENTRY.write_json(manifest, {"schema": "ani.finetune.base-manifest.v1",
                                           "files": ENTRY.inventory(workspace / "inputs/model")})
                configuration = ENTRY.projection(
                    parameters={"cutoff_len": 96, "gradient_accumulation_steps": 2,
                                "learning_rate": "0.00013", "lora_alpha": 12,
                                "lora_dropout": "0.05", "lora_rank": 4, "max_steps": 11,
                                "per_device_train_batch_size": 1, "seed": 73},
                    device="cpu",
                )
                config_path = workspace / "inputs/finetune_configuration.json"
                ENTRY.write_json(config_path, configuration)
                args = argparse.Namespace(
                    workspace=workspace, configuration=config_path,
                    expected_input_bytes=len(raw),
                    expected_input_sha256=hashlib.sha256(raw).hexdigest(),
                    expected_base_manifest_sha256=hashlib.sha256(manifest.read_bytes()).hexdigest(),
                )

                def observe_configuration(*parsed, callbacks):
                    self.assertEqual(parsed[0].compute_dtype, torch.float32)
                    self.assertEqual(ENTRY.effective_projection(parsed), configuration)
                    raise ParsedConfigurationObserved

                # The framework's dtype-capability leaves are the only controlled
                # environmental inputs. The real train entrypoint runs through
                # parser, tokenizer, patch_config and its strict projection check.
                # Only external run_sft is stopped before training side effects.
                with (
                    patch.object(misc, "_is_bf16_available", bf16),
                    patch.object(misc, "_is_fp16_available", fp16),
                    patch.object(sft, "run_sft", side_effect=observe_configuration),
                    self.assertRaises(ParsedConfigurationObserved),
                ):
                    ENTRY.train(args)
                summary = json.loads((workspace / "reports/train/summary.json").read_bytes())
                self.assertEqual(summary["effective_configuration"], configuration)
                self.assertEqual(summary["optimizer_steps"], 0)
                self.assertEqual(summary["status"], "FAILED")


if __name__ == "__main__":
    unittest.main()
