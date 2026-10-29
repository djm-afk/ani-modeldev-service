"""Check the current projection against the actual pinned native parser/patcher.

ANI_FINETUNE_TEST_BASE points at a fixed local tokenizer/config fixture. No model
weights, dataset conversion or optimizer execution is replaced by a test double.
The capability leaves are controlled inputs, not GPU hardware acceptance.
"""

import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "finetune_entry", Path(__file__).parents[1] / "finetuning/finetune_entry.py"
)
ENTRY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ENTRY)


@unittest.skipUnless(os.environ.get("ANI_FINETUNE_TEST_BASE"), "requires the actual pinned runtime and local model fixture")
class FrameworkConfigurationTests(unittest.TestCase):
    def test_native_and_persisted_jinja_token_ids_match(self):
        from llamafactory.data import template as native
        from llamafactory.hparams import get_train_args
        from transformers import AutoTokenizer

        base = Path(os.environ["ANI_FINETUNE_TEST_BASE"])
        for identifier in ("empty", "alpaca"):
            with self.subTest(template=identifier), tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"]) as directory:
                configuration = ENTRY.projection(steps=3, cutoff_len=64)
                parsed = get_train_args(ENTRY.factory_arguments(Path(directory), configuration, Path(directory) / "output"))
                tokenizer = AutoTokenizer.from_pretrained(base, local_files_only=True, trust_remote_code=False)
                parsed[1].template = identifier
                template = native.get_template_and_fix_tokenizer(tokenizer, parsed[1])
                tokenizer.chat_template = template._get_jinja_template(tokenizer)
                probes = ENTRY.encoding_probes(tokenizer, template)
                self.assertEqual(len(probes), 4)
                self.assertTrue(all(probe["target_tokens"] > 0 for probe in probes))

    def test_frozen_float32_survives_capability_selection(self):
        import torch
        from llamafactory.extras import misc
        from llamafactory.hparams import get_train_args
        from llamafactory.model.loader import load_config, load_tokenizer
        from llamafactory.model.patcher import patch_config

        base = Path(os.environ["ANI_FINETUNE_TEST_BASE"])
        self.assertTrue((base / "config.json").is_file())
        self.assertFalse(torch.cuda.is_available(), "capability replay runs without a GPU")
        for scenario, bf16, fp16 in (("cpu", False, False), ("bf16_capability", True, True), ("fp16_capability", False, True)):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"]) as directory:
                configuration = ENTRY.projection(
                    parameters={"cutoff_len": 64, "gradient_accumulation_steps": 2,
                                "learning_rate": "0.00013", "lora_alpha": 12,
                                "lora_dropout": "0.05", "lora_rank": 4, "max_steps": 11,
                                "per_device_train_batch_size": 1, "seed": 73},
                    device="cpu",
                )
                arguments = ENTRY.factory_arguments(Path(directory), configuration, Path(directory) / "output")
                arguments["model_name_or_path"] = str(base)
                with patch.object(misc, "_is_bf16_available", bf16), patch.object(misc, "_is_fp16_available", fp16):
                    parsed = get_train_args(arguments)
                    parsed[0].compute_dtype = getattr(torch, configuration["managed_settings"]["train_dtype"])
                    tokenizer = load_tokenizer(parsed[0])["tokenizer"]
                    model_config = load_config(parsed[0])
                    patch_config(model_config, tokenizer, parsed[0], {}, is_trainable=True)
                self.assertEqual(parsed[0].compute_dtype, torch.float32)
                self.assertEqual(ENTRY.effective_projection(parsed), configuration)
                self.assertEqual(Path(arguments["cache_dir"]), Path(os.environ["HF_DATASETS_CACHE"]))


if __name__ == "__main__":
    unittest.main()
