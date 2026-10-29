"""The real managed native SFT seam enforces its frozen training precision.

Run in the pinned training runtime. A tiny locally created BF16 CausalLM and
tokenizer exercise native conversion, LoRA, Trainer, optimizer and adapter save;
no model download or platform business operation is involved.
"""
import importlib.util
import json
import math
import os
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    'native_dtype_entry', Path(__file__).parents[1] / 'finetuning/finetune_entry.py')
ENTRY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ENTRY)


@unittest.skipUnless(os.environ.get('ANI_FINETUNE_TEST_BASE'),
                     'requires the pinned runtime and a fixed local tokenizer fixture')
class NativeTrainingDtypeTest(unittest.TestCase):
    def test_bf16_base_trains_real_lora_steps_in_frozen_float32(self):
        original_environment = dict(os.environ)
        try:
            with tempfile.TemporaryDirectory(prefix='native-dtype-', dir=os.environ['TMPDIR']) as directory:
                root = Path(directory)
                ENTRY.configure_work_directory(root, 'train')
                import torch
                from safetensors import safe_open
                from transformers import (AutoTokenizer, Qwen2Config,
                                          Qwen2ForCausalLM, TrainerCallback)
                from llamafactory.data import template as native_template
                from llamafactory.hparams import get_train_args
                from llamafactory.model.loader import load_tokenizer
                from native_stages import run_managed_sft

                torch.set_num_threads(1)
                torch.manual_seed(17)
                base = root / 'inputs/model'
                base.mkdir(parents=True)
                tokenizer = AutoTokenizer.from_pretrained(
                    os.environ['ANI_FINETUNE_TEST_BASE'], local_files_only=True,
                    trust_remote_code=False)
                tokenizer.save_pretrained(base)
                config = Qwen2Config(vocab_size=len(tokenizer), hidden_size=8,
                                    intermediate_size=16, num_hidden_layers=1,
                                    num_attention_heads=2, num_key_value_heads=2,
                                    max_position_embeddings=256, pad_token_id=tokenizer.eos_token_id,
                                    bos_token_id=tokenizer.bos_token_id,
                                    eos_token_id=tokenizer.eos_token_id, tie_word_embeddings=False)
                Qwen2ForCausalLM(config).to(dtype=torch.bfloat16).save_pretrained(base)
                with safe_open(base / 'model.safetensors', framework='pt') as saved:
                    self.assertEqual({saved.get_slice(k).get_dtype() for k in saved.keys()}, {'BF16'})

                data = root / 'inputs/data'
                data.mkdir()
                rows = [{'instruction': 'Name color', 'input': '', 'output': answer}
                        for answer in ('Blue', 'Red')]
                (data / 'finetune_data.jsonl').write_text(
                    ''.join(json.dumps(row) + '\n' for row in rows))
                (data / 'dataset_info.json').write_text(json.dumps({
                    'ani_cpu_smoke': {'file_name': 'finetune_data.jsonl', 'formatting': 'alpaca',
                                      'columns': {'prompt': 'instruction', 'query': 'input',
                                                  'response': 'output'}}}))
                configuration = ENTRY.projection(parameters={
                    'max_steps': 2, 'cutoff_len': 128, 'per_device_train_batch_size': 1,
                    'gradient_accumulation_steps': 1, 'lora_rank': 2, 'lora_alpha': 4,
                    'lora_dropout': '0', 'learning_rate': '0.001', 'seed': 17})
                output = root / 'adapter'
                parsed = get_train_args(ENTRY.factory_arguments(root, configuration, output))
                parsed[0].compute_dtype = torch.float32
                parsed[1].template = 'smollm2'
                tokenizer_module = load_tokenizer(parsed[0])
                template = native_template.get_template_and_fix_tokenizer(
                    tokenizer_module['tokenizer'], parsed[1])
                observations = []
                test = self

                class Observe(TrainerCallback):
                    optimizer_steps = 0

                    def on_train_begin(self, args, state, control, model=None, **kwargs):
                        test.assertEqual(observations[0]['samples'], 2)
                        test.assertEqual({p.dtype for p in model.parameters()}, {torch.float32},
                                         'frozen float32 must reach the real Trainer, including base weights')
                        self.model = model
                        self.initial = {n: p.detach().clone() for n, p in model.named_parameters()
                                        if p.requires_grad}
                        test.assertTrue(self.initial, 'native LoRA must expose trainable parameters')
                        test.assertTrue(all('lora_' in name for name in self.initial))

                    def on_optimizer_step(self, args, state, control, **kwargs):
                        self.optimizer_steps += 1

                callback = Observe()
                measurements = ENTRY.PhaseMeasurements(root, 'train')
                result = run_managed_sft(parsed, tokenizer_module=tokenizer_module,
                                         template=template, callbacks=[callback],
                                         observe_dataset=observations.append, measurements=measurements)
                self.assertEqual(result.global_step, 2)
                self.assertEqual(callback.optimizer_steps, 2)
                self.assertTrue(math.isfinite(result.training_loss))
                final = dict(callback.model.named_parameters())
                self.assertTrue(any(not torch.equal(before, final[name].detach())
                                    for name, before in callback.initial.items()),
                                'the real optimizer must update a LoRA parameter')
                with safe_open(output / 'adapter_model.safetensors', framework='pt') as saved:
                    self.assertTrue(list(saved.keys()))
                    self.assertEqual({saved.get_slice(k).get_dtype() for k in saved.keys()}, {'F32'})
                outcomes = {phase['name']: phase['outcome']
                            for phase in measurements.report()['phases']}
                self.assertEqual(outcomes['optimizer'], 'SUCCEEDED')
                self.assertEqual(outcomes['adapter_save'], 'SUCCEEDED')
        finally:
            os.environ.clear()
            os.environ.update(original_environment)


if __name__ == '__main__':
    unittest.main()
