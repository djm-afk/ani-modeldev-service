"""Test-only observation of the final image's actual local-JSONL preprocessing.

Mount this file read-only into each final image. Fixture creation is a separate
invocation with writable inputs; measurement uses read-only inputs and only the
role work mount. No hook is copied into the production image.
"""

import argparse
import hashlib
import inspect
import json
import os
from pathlib import Path
import sys

TRAINING = Path('/opt/ani-modeldev/training')
sys.path.insert(0, str(TRAINING))
sys.path.insert(0, str(TRAINING / 'finetuning'))
from template_runtime import configure_work_directory
from phase_measurement import PhaseMeasurements


def fixture(workspace):
    from tokenizers import Tokenizer, models, pre_tokenizers
    from transformers import LlamaConfig, PreTrainedTokenizerFast

    inputs = workspace / 'inputs'
    inputs.mkdir()
    model = inputs / 'model'
    model.mkdir()
    tokens = {'<unk>': 0, '<bos>': 1, '<eos>': 2, '<pad>': 3,
              'hello': 4, 'world': 5, 'yes': 6, 'again': 7}
    backend = Tokenizer(models.WordLevel(tokens, unk_token='<unk>'))
    backend.pre_tokenizer = pre_tokenizers.Whitespace()
    tokenizer = PreTrainedTokenizerFast(tokenizer_object=backend, unk_token='<unk>',
        bos_token='<bos>', eos_token='<eos>', pad_token='<pad>', model_max_length=64)
    tokenizer.save_pretrained(model)
    LlamaConfig(vocab_size=len(tokens), hidden_size=8, intermediate_size=16,
        num_hidden_layers=1, num_attention_heads=1, num_key_value_heads=1,
        max_position_embeddings=64, bos_token_id=1, eos_token_id=2, pad_token_id=3).save_pretrained(model)
    data = inputs / 'data'
    data.mkdir()
    rows = [{'instruction': 'hello', 'input': 'world', 'output': 'yes'},
            {'instruction': 'hello again', 'input': 'world', 'output': 'yes'}]
    (data / 'finetune_data.jsonl').write_text(''.join(json.dumps(x) + '\n' for x in rows))
    (data / 'dataset_info.json').write_text(json.dumps({'ani_cpu_smoke': {
        'file_name': 'finetune_data.jsonl', 'formatting': 'alpaca',
        'columns': {'prompt': 'instruction', 'query': 'input', 'response': 'output'}}}))
    print(json.dumps({'state': 'FIXTURE_CREATED', 'records': len(rows),
                      'scope': 'native preprocessing mechanism, no model training'}))


def measure(workspace, expected_sha):
    from datasets import Dataset
    from datasets.arrow_writer import ArrowWriter
    from transformers import AutoTokenizer
    from llamafactory.data import converter, get_template_and_fix_tokenizer
    from llamafactory.hparams import get_train_args
    from configuration import projection
    from finetune_entry import factory_arguments
    from native_stages import run_managed_sft

    work = workspace / 'work/train'
    converter_path = Path(converter.__file__).resolve()
    converter_sha = hashlib.sha256(converter_path.read_bytes()).hexdigest()
    if converter_sha != expected_sha:
        raise ValueError('the actual imported converter SHA256 differs')
    arguments = factory_arguments(workspace, projection(steps=1, cutoff_len=64), work / 'native')
    arguments['template'] = 'empty'
    parsed = get_train_args(arguments)
    tokenizer = AutoTokenizer.from_pretrained(workspace / 'inputs/model',
        local_files_only=True, trust_remote_code=False)
    template = get_template_and_fix_tokenizer(tokenizer, parsed[1])
    original_map = Dataset.map
    original_write = ArrowWriter.write
    original_flush = ArrowWriter.write_examples_on_file
    active = False
    calls, queues = [], []
    signature = inspect.signature(original_map)

    def caches(dataset):
        result = []
        for item in dataset.cache_files:
            path = Path(item['filename'])
            if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(work.resolve()):
                raise ValueError('native cache is outside the execution role work root')
            result.append({'path': str(path.resolve()), 'bytes': path.stat().st_size})
        if not result:
            raise ValueError('native source/aligned/tokenized cache_files must be nonempty')
        return result

    def observed_map(self, *args, **kwargs):
        nonlocal active
        bound = signature.bind(self, *args, **kwargs)
        bound.apply_defaults()
        name = kwargs.get('desc', '')
        conversion = name == 'Converting format of dataset'
        tokenization = name.startswith('Running tokenizer on dataset')
        record = None
        if conversion or tokenization:
            function = bound.arguments['function']
            record = {'kind': 'conversion' if conversion else 'tokenization',
                'function_module': getattr(function, '__module__', type(function).__module__),
                'function_type': type(function).__qualname__, 'batched': bound.arguments['batched'],
                'batch_size': bound.arguments['batch_size'],
                'writer_batch_size': bound.arguments['writer_batch_size'],
                'keep_in_memory': bound.arguments['keep_in_memory'],
                'source_cache_files': caches(self)}
            calls.append(record)
        prior = active
        active = conversion
        try:
            result = original_map(self, *args, **kwargs)
        finally:
            active = prior
        if record is not None:
            record['output_cache_files'] = caches(result)
        return result

    def observe_queue(writer, where):
        if active:
            queues.append({'where': where, 'pending_records': len(writer.current_examples),
                           'writer_batch_size': writer.writer_batch_size})

    def observed_write(self, *args, **kwargs):
        value = original_write(self, *args, **kwargs)
        observe_queue(self, 'after_write')
        return value

    def observed_flush(self, *args, **kwargs):
        # The original writer has already enqueued its examples here. Observe
        # before it flushes; the post-write queue alone can be empty at batch 1.
        observe_queue(self, 'after_enqueue_before_flush')
        return original_flush(self, *args, **kwargs)

    class PreprocessingObserved(Exception):
        pass

    observed = {}

    def done(facts):
        observed.update(facts)
        raise PreprocessingObserved()

    Dataset.map, ArrowWriter.write, ArrowWriter.write_examples_on_file = observed_map, observed_write, observed_flush
    try:
        try:
            run_managed_sft(parsed, tokenizer_module={'tokenizer': tokenizer, 'processor': None},
                            template=template, callbacks=[], observe_dataset=done,
                            measurements=PhaseMeasurements(workspace, 'train'))
        except PreprocessingObserved:
            pass
    finally:
        Dataset.map, ArrowWriter.write, ArrowWriter.write_examples_on_file = original_map, original_write, original_flush
    conversion = [x for x in calls if x['kind'] == 'conversion']
    tokenization = [x for x in calls if x['kind'] == 'tokenization']
    preflush = [x for x in queues if x['where'] == 'after_enqueue_before_flush']
    peak = max((x['pending_records'] for x in queues), default=-1)
    valid = (len(conversion) == len(tokenization) == 1 and len(preflush) >= 2
        and conversion[0]['writer_batch_size'] == 1 and conversion[0]['batched'] is False
        and tokenization[0]['batched'] is True and tokenization[0]['batch_size'] == 1
        and peak == 1 and observed.get('samples') == 2
        and all(x['keep_in_memory'] is False for x in calls))
    result = {'state': 'PASS' if valid else 'FAIL', 'converter_path': str(converter_path),
        'converter_sha256': converter_sha, 'native_map_calls': calls,
        'conversion_writer_observations': queues, 'conversion_peak_pending_records': peak,
        'native_preprocessing': observed, 'scope': 'actual native preprocessing; no optimizer or platform acceptance'}
    (work / 'arrow-mechanism.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
    if not valid:
        raise ValueError('native conversion writer, tokenizer batch or disk-cache mechanism differs')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--create-fixture', action='store_true')
    parser.add_argument('--expected-converter-sha256')
    args = parser.parse_args()
    workspace = Path('/workspace')
    configure_work_directory(workspace, 'train')
    if args.create_fixture:
        fixture(workspace)
    else:
        if not args.expected_converter_sha256:
            parser.error('the expected imported source SHA256 is required')
        measure(workspace, args.expected_converter_sha256)


if __name__ == '__main__':
    main()
