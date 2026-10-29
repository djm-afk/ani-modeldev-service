"""Explicit composition of the pinned native SFT/LoRA stages.

This is the managed train/export subset of LLaMA-Factory 0.9.6.dev0's
train/sft/workflow.py and train/tuner.py. Tokenizer and template are mandatory
inputs. Native dataset conversion/tokenization, model/PEFT initialization,
collator, Trainer optimizer and model saving remain the actual implementations.
"""
import os
from pathlib import Path

from template_runtime import RuntimeFailure


def _disk_cache_files(dataset):
    root = Path(os.environ['TMPDIR']).parent.resolve()
    files = dataset.cache_files
    if not files:
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'native dataset has no disk cache')
    result = []
    for item in files:
        path = Path(item['filename'])
        if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(root):
            raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'dataset cache is outside this execution role')
        result.append(str(path.resolve()))
    return result


def _truncation_facts(dataset, template, tokenizer, processor, data_args):
    from llamafactory.data.processor.processor_utils import infer_seqlen
    count, removed_tokens = 0, 0
    for row in dataset:
        # The native strict Alpaca converter produces this exact text-only
        # shape. Do not create a second converter or retain token tables.
        prompt, response = row['_prompt'], row['_response']
        if len(prompt) % 2 != 1 or len(response) != 1:
            continue
        images, videos, audios = row['_images'] or [], row['_videos'] or [], row['_audios'] or []
        messages = template.mm_plugin.process_messages(prompt + response, images, videos, audios, processor)
        prefix, _ = template.mm_plugin.process_token_ids([], [], images, videos, audios, tokenizer, processor)
        pairs = template.encode_multiturn(tokenizer, messages, row['_system'], row['_tools'],
                                         data_args.mask_history and not template.preserve_thinking)
        total = len(prefix) + int(template.efficient_eos)
        if data_args.mask_history:
            pairs = pairs[::-1]
        for source, target in pairs:
            if total >= data_args.cutoff_len:
                break
            source_len, target_len = infer_seqlen(len(source), len(target), data_args.cutoff_len - total)
            removed = len(source) + len(target) - source_len - target_len
            count += int(removed > 0)
            removed_tokens += removed
            total += source_len + target_len
    return count, removed_tokens


def run_managed_sft(parsed, *, tokenizer_module, template, callbacks, observe_dataset, measurements):
    from llamafactory.data import SFTDataCollatorWith4DAttentionMask
    from llamafactory.data.loader import _get_merged_dataset, _get_preprocessed_dataset
    from llamafactory.extras.constants import IGNORE_INDEX
    from llamafactory.model import load_model
    from llamafactory.train.sft.trainer import CustomSeq2SeqTrainer

    model_args, data_args, training_args, finetuning_args, generating_args = parsed
    if (data_args.streaming or data_args.packing or data_args.max_samples is not None
            or data_args.tokenized_path is not None or data_args.eval_dataset is not None
            or data_args.val_size != 0 or data_args.preprocessing_num_workers != 1
            or data_args.preprocessing_batch_size != 1 or not data_args.overwrite_cache
            or training_args.dataloader_num_workers != 0 or training_args.do_eval
            or training_args.do_predict or not training_args.do_train or finetuning_args.use_asft_loss):
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'native parsed stages differ from managed SFT')
    tokenizer, processor = tokenizer_module['tokenizer'], tokenizer_module['processor']
    with measurements.phase('load_convert'):
        with training_args.main_process_first(desc='load dataset', local=not data_args.data_shared_file_system):
            aligned = _get_merged_dataset(data_args.dataset, model_args, data_args, training_args, 'sft')
    aligned_cache = _disk_cache_files(aligned)
    measurements.record_dataset_cache_files('aligned', aligned_cache)
    with measurements.phase('truncate'):
        truncated_samples, truncated_tokens = _truncation_facts(aligned, template, tokenizer, processor, data_args)
    with measurements.phase('tokenize'):
        with training_args.main_process_first(desc='pre-process dataset', local=not data_args.data_shared_file_system):
            dataset = _get_preprocessed_dataset(aligned, data_args, training_args, 'sft', template, tokenizer, processor, is_eval=False)
    tokenized_cache = _disk_cache_files(dataset)
    measurements.record_dataset_cache_files('tokenized', tokenized_cache)
    if not len(dataset):
        raise RuntimeFailure('INPUT_INVALID', 'no effective training samples')
    effective_tokens, at_cutoff, maximum, observed = 0, 0, 0, 0
    for row in dataset:
        length = len(row['input_ids'])
        if not 1 <= length <= data_args.cutoff_len:
            raise RuntimeFailure('INPUT_INVALID', 'native preprocessing violates cutoff_len')
        effective_tokens += length
        at_cutoff += int(length == data_args.cutoff_len)
        maximum = max(maximum, length)
        observed += 1
    if observed != len(dataset):
        raise RuntimeFailure('INPUT_INVALID', 'native dataset count changed')
    observe_dataset({'samples': observed, 'effective_token_count': effective_tokens,
                     'samples_at_cutoff': at_cutoff, 'max_effective_tokens': maximum,
                     'truncated_samples': truncated_samples, 'truncated_tokens': truncated_tokens,
                     'aligned_cache_files': aligned_cache, 'tokenized_cache_files': tokenized_cache})
    with measurements.phase('model_load'):
        model = load_model(tokenizer, model_args, finetuning_args, training_args.do_train)
        # The native loader selects checkpoint precision with dtype="auto";
        # enforce the frozen training precision before Trainer sees parameters.
        model = model.to(dtype=model_args.compute_dtype)
        collator = SFTDataCollatorWith4DAttentionMask(
            template=template, model=model, pad_to_multiple_of=8,
            label_pad_token_id=IGNORE_INDEX if data_args.ignore_pad_token_for_loss else tokenizer.pad_token_id,
            block_diag_attn=model_args.block_diag_attn, neat_packing=data_args.neat_packing,
            attn_implementation=getattr(model.config, '_attn_implementation', None),
            compute_dtype=model_args.compute_dtype, **tokenizer_module)
        generation = generating_args.to_dict(obey_generation_config=True)
        generation['eos_token_id'] = list(dict.fromkeys(template.get_stop_token_ids(tokenizer)))
        generation['pad_token_id'] = tokenizer.pad_token_id
        trainer = CustomSeq2SeqTrainer(model=model, args=training_args, finetuning_args=finetuning_args,
                                      data_collator=collator, callbacks=callbacks, gen_kwargs=generation,
                                      ref_model=None, train_dataset=dataset, **tokenizer_module)
    with measurements.phase('optimizer'):
        result = trainer.train(resume_from_checkpoint=training_args.resume_from_checkpoint)
    with measurements.phase('adapter_save'):
        trainer.save_model()
        trainer.log_metrics('train', result.metrics)
        trainer.save_metrics('train', result.metrics)
        trainer.save_state()
    return result


def export_managed(args, *, tokenizer_module, effective_template):
    import torch
    from transformers import PreTrainedModel
    from llamafactory.hparams import get_infer_args
    from llamafactory.model import load_model

    model_args, _data_args, finetuning_args, _generating_args = get_infer_args(args)
    if model_args.export_dir is None or model_args.export_device != 'cpu' or model_args.infer_dtype != 'float32':
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'export must use frozen float32 CPU settings')
    tokenizer = tokenizer_module['tokenizer']
    model = load_model(tokenizer, model_args, finetuning_args)
    if not isinstance(model, PreTrainedModel) or getattr(model, 'quantization_method', None) is not None:
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'native unquantized CausalLM merge is required')
    model.config.torch_dtype = torch.float32
    model = model.to(dtype=torch.float32, device='cpu')
    if model.get_input_embeddings().weight.shape[0] < len(tokenizer):
        raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'merged embeddings omit effective vocabulary')
    model.generation_config.eos_token_id = effective_template['eos_ids']
    model.generation_config.pad_token_id = tokenizer.pad_token_id
    model.generation_config.bos_token_id = tokenizer.bos_token_id
    model.save_pretrained(save_directory=model_args.export_dir, max_shard_size=f'{model_args.export_size}GB')
    tokenizer.save_pretrained(model_args.export_dir)
