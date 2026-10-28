"""Consume the single frozen template/model contract through native runtime APIs."""
import ast
import hashlib
import importlib.metadata
import inspect
import json
import os
from pathlib import Path
import re
import sys

from configuration import canonical
from dataset_manifest import STRUCTURE_LIMITS, unique_fields

INT64_MAX = 9223372036854775807
PACKAGES = ('llamafactory', 'torch', 'transformers', 'datasets', 'accelerate', 'peft', 'numpy', 'pyarrow')


class RuntimeFailure(ValueError):
    def __init__(self, reason, detail):
        self.reason = reason
        super().__init__(reason + ': ' + detail)


def configure_work_directory(workspace, role):
    if role not in ('train', 'export', 'verify'):
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'unknown stage')
    root = Path(os.path.abspath(workspace)) / 'work' / role
    for suffix in ('tmp', 'hf', 'hf/datasets', 'matplotlib'):
        target = root / suffix
        for part in (*reversed(target.parents), target):
            if part.is_symlink():
                raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'linked work directory')
        target.mkdir(parents=True, exist_ok=True)
    for name, suffix in (('TMPDIR', 'tmp'), ('HF_HOME', 'hf'), ('HF_DATASETS_CACHE', 'hf/datasets'), ('MPLCONFIGDIR', 'matplotlib')):
        os.environ[name] = str(root / suffix)
    os.environ['HF_HUB_OFFLINE'] = '1'
    os.environ['TRANSFORMERS_OFFLINE'] = '1'
    return root


def resolve_context_limit(objects, fields):
    values, seen = [], set()
    for item in fields:
        source, field = item['source'], item['field']
        if source not in ('config.json', 'tokenizer_config.json') or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', field):
            raise RuntimeFailure('BASE_MODEL_INVALID', 'invalid context source')
        if (source, field) in seen:
            continue
        seen.add((source, field))
        if field not in objects[source]:
            continue
        value = objects[source][field]
        if type(value) is not int or value <= 0:
            raise RuntimeFailure('BASE_MODEL_INVALID', 'declared context must be a positive JSON integer')
        if source == 'tokenizer_config.json' and field == 'model_max_length' and value >= INT64_MAX:
            continue
        if value > INT64_MAX:
            raise RuntimeFailure('BASE_MODEL_INVALID', 'context exceeds int64')
        values.append(value)
    if not values:
        raise RuntimeFailure('CONTEXT_LIMIT_UNKNOWN', 'no finite model/tokenizer context')
    return min(values)


def read_json(path, maximum, read_regular, expected=None):
    raw = read_regular(path, maximum)
    if expected is not None and hashlib.sha256(raw).hexdigest() != expected:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'frozen input SHA256 differs')
    value = json.loads(raw.decode('utf-8'), object_pairs_hook=unique_fields,
                       parse_constant=lambda _: (_ for _ in ()).throw(ValueError('nonfinite JSON')))
    if canonical(value) != raw:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'input is not current canonical JSON')
    return value


def load_runtime_plan(workspace, expected, read_regular):
    plan = read_json(workspace / 'inputs/template-plan.json', STRUCTURE_LIMITS['MaxManifestBytes'], read_regular, expected)
    if plan.get('schema_version') != 'ani.modeldev.template-plan.v3':
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'unknown template plan')
    caps = read_json(workspace / 'inputs/runtime-capabilities.json', STRUCTURE_LIMITS['MaxMaterialBytes'], read_regular, plan['runtime_capabilities_sha256'])
    if caps.get('schema_version') != 'ani.modeldev.finetuning-capabilities.v3' or caps['image_digest'] != plan['image_digest']:
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'runtime identity differs')
    actual_entry = Path(__file__).with_name('finetune_entry.py').read_bytes()
    if hashlib.sha256(actual_entry).hexdigest() != caps['entrypoint_sha256']:
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'platform entrypoint SHA256 differs')
    for package in caps['packages']:
        if importlib.metadata.version(package['name']) != package['version']:
            raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'package version differs: ' + package['name'])
    return plan, caps


def verify_context_facts(workspace, facts, caps, read_regular):
    objects = {}
    for name, key in (('config.json', 'config_file'), ('tokenizer_config.json', 'tokenizer_config_file')):
        raw = read_regular(workspace / 'inputs/model' / name, STRUCTURE_LIMITS['MaxModelConfigBytes'])
        identity = facts[key]
        if identity['path'] != name or str(len(raw)) != identity['size_bytes'] or hashlib.sha256(raw).hexdigest() != identity['sha256']:
            raise RuntimeFailure('BASE_MODEL_INVALID', 'model config identity differs')
        objects[name] = json.loads(raw.decode('utf-8'), object_pairs_hook=unique_fields)
    if objects['config.json'].get('model_type') != facts['model_type'] or facts['model_type'] not in caps['causal_lm_model_types']:
        raise RuntimeFailure('BASE_MODEL_UNSUPPORTED', 'model_type is outside the actual CausalLM mapping')
    limit = resolve_context_limit(objects, caps['context_limit_fields'])
    if str(limit) != facts['context_limit']:
        raise RuntimeFailure('BASE_MODEL_INVALID', 'normalized context differs')
    return limit


def registered_template_sources(module):
    source = inspect.getsource(module)
    result = {}
    for node in ast.walk(ast.parse(source)):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Name) or node.func.id != 'register_template':
            continue
        names = [k.value.value for k in node.keywords if k.arg == 'name' and isinstance(k.value, ast.Constant) and type(k.value.value) is str]
        if len(names) == 1:
            result[names[0]] = hashlib.sha256(ast.get_source_segment(source, node).encode()).hexdigest()
    if set(result) != set(module.TEMPLATES):
        raise RuntimeFailure('RUNTIME_UNSUPPORTED', 'native template registry has an unaccounted definition')
    return result


def source_template(workspace, source, read_regular):
    raw = read_regular(workspace / 'inputs/model' / source['path'], STRUCTURE_LIMITS['MaxModelConfigBytes'])
    if source['kind'] == 'FILE':
        body = raw.decode('utf-8')
    elif source['kind'] == 'EMBEDDED' and source['path'] == 'tokenizer_config.json':
        value = json.loads(raw.decode('utf-8'), object_pairs_hook=unique_fields)['chat_template']
        if type(value) is str:
            body = value
        elif type(value) is dict:
            body = value[source['name']]
        elif type(value) is list:
            matches = [v['template'] for v in value if v['name'] == source['name']]
            if len(matches) != 1:
                raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'native template selection is ambiguous')
            body = matches[0]
        else:
            raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'invalid embedded template')
    else:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'unsupported template source')
    encoded = body.encode('utf-8')
    if not encoded or len(encoded) != int(source['size_bytes']) or hashlib.sha256(encoded).hexdigest() != source['sha256']:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'native template source differs')
    return body


def encoding_probes(tokenizer, template):
    results = []
    for instruction, query, response in (('Name one color.', '', 'Blue.'), ('Answer this.', 'What is 2 + 2?', '4.'), ('请回答。', 'café🙂', '可以。'), ('Boundary', '', tokenizer.eos_token)):
        prompt = instruction + ('\n' + query if query else '')
        messages = [{'role': 'user', 'content': prompt}, {'role': 'assistant', 'content': response}]
        source_ids, target_ids = template.encode_oneturn(tokenizer, messages)
        expected = source_ids + target_ids + ([tokenizer.eos_token_id] if template.efficient_eos else [])
        actual = tokenizer.apply_chat_template(messages, tokenize=True, add_generation_prompt=False, return_dict=False)
        boundary = tokenizer.apply_chat_template(messages[:1], tokenize=True, add_generation_prompt=True, return_dict=False)
        if list(actual) != expected or list(boundary) != source_ids:
            raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'native formatter and exported tokenizer differ')
        results.append({'input_tokens': len(source_ids), 'target_tokens': len(expected)-len(source_ids), 'sha256': hashlib.sha256(canonical(expected)).hexdigest()})
    return results


def token_facts(tokenizer, template):
    tokens = []
    for name, value in tokenizer.special_tokens_map.items():
        entries = value if isinstance(value, list) else [value]
        for index, token in enumerate(entries):
            text = str(token)
            identifier = tokenizer.convert_tokens_to_ids(text)
            if type(identifier) is not int or not 0 <= identifier < len(tokenizer):
                raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'special token outside vocabulary')
            tokens.append({'name': name + ('.' + str(index) if isinstance(value, list) else ''), 'token': text, 'id': str(identifier)})
    eos = sorted(set(template.get_stop_token_ids(tokenizer)))
    declared = {int(v['id']) for v in tokens}
    if not eos or any(type(v) is not int or v not in declared or not 0 <= v < len(tokenizer) for v in eos):
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'EOS facts are outside special-token vocabulary')
    return sorted(tokens, key=lambda v: v['name']), eos


def resolve_effective_template(workspace, plan, tokenizer, data_args, read_regular):
    from llamafactory.data import template as native
    if plan['mode'] == 'NAMED':
        identifier = plan['template_id']
        if registered_template_sources(native).get(identifier) != plan['source_sha256']:
            raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'named native definition differs')
        data_args.template = identifier
    elif plan['mode'] == 'TOKENIZER_NATIVE':
        tokenizer.chat_template = source_template(workspace, plan['source'], read_regular)
        data_args.template = None
    else:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'unknown template mode')
    try:
        template = native.get_template_and_fix_tokenizer(tokenizer, data_args)
        # Native API may retain the source tokenizer's unrelated Jinja for a
        # named formatter. Force the formatter's actual export representation.
        if plan['mode'] == 'NAMED':
            tokenizer.chat_template = template._get_jinja_template(tokenizer)
        body = tokenizer.chat_template.encode('utf-8')
        if not 0 < len(body) <= STRUCTURE_LIMITS['MaxModelConfigBytes']:
            raise RuntimeFailure('TEMPLATE_PARSE_FAILED', 'effective template body size')
        encoding_probes(tokenizer, template)
        tokens, eos = token_facts(tokenizer, template)
    except RuntimeFailure:
        raise
    except Exception as error:
        raise RuntimeFailure('TEMPLATE_PARSE_FAILED', type(error).__name__) from error
    report = {'schema_version': 'ani.modeldev.effective-template.v3',
              'template_plan_sha256': hashlib.sha256(canonical(plan)).hexdigest(),
              'runtime_capabilities_sha256': plan['runtime_capabilities_sha256'],
              'image_digest': plan['image_digest'], 'source_sha256': plan['source_sha256'],
              'template_file': {'path': 'reports/train/effective-template.jinja', 'size_bytes': str(len(body)), 'sha256': hashlib.sha256(body).hexdigest()},
              'vocab_size': str(len(tokenizer)), 'special_tokens': tokens, 'eos_ids': eos,
              'encoding_options': {'add_bos': bool(getattr(tokenizer, 'add_bos_token', False)), 'add_eos': template.efficient_eos, 'replace_eos': template.replace_eos}}
    return template, report, body


def assert_effective_tokenizer(tokenizer, report, body):
    if (tokenizer.chat_template != body.decode('utf-8') or len(tokenizer) != int(report['vocab_size'])):
        raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'reloaded tokenizer template/vocabulary differs')
    declared = {}
    for name, value in tokenizer.special_tokens_map.items():
        entries = value if isinstance(value, list) else [value]
        for index, token in enumerate(entries):
            key = name + ('.' + str(index) if isinstance(value, list) else '')
            declared[key] = (str(token), str(tokenizer.convert_tokens_to_ids(str(token))))
    expected = {v['name']: (v['token'], v['id']) for v in report['special_tokens']}
    if declared != expected:
        raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'reloaded special tokens differ')


def consume_effective_template(workspace, plan, tokenizer, read_regular):
    report = read_json(workspace / 'reports/train/effective-template.json', STRUCTURE_LIMITS['MaxManifestBytes'], read_regular)
    if (report.get('schema_version') != 'ani.modeldev.effective-template.v3'
            or report['template_plan_sha256'] != hashlib.sha256(canonical(plan)).hexdigest()
            or any(report[k] != plan[k] for k in ('runtime_capabilities_sha256', 'image_digest', 'source_sha256'))
            or report['template_file']['path'] != 'reports/train/effective-template.jinja'):
        raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'effective template lineage differs')
    body = read_regular(workspace / report['template_file']['path'], STRUCTURE_LIMITS['MaxModelConfigBytes'])
    if str(len(body)) != report['template_file']['size_bytes'] or hashlib.sha256(body).hexdigest() != report['template_file']['sha256']:
        raise RuntimeFailure('TEMPLATE_ENCODING_MISMATCH', 'effective Jinja differs')
    assert_effective_tokenizer(tokenizer, report, body)
    from llamafactory.data.template import parse_template
    # Parse the persisted effective body, never choose a source/default template.
    template = parse_template(tokenizer)
    special = {int(v['id']): v['token'] for v in report['special_tokens']}
    template.stop_words = [special[v] for v in report['eos_ids'] if v != tokenizer.eos_token_id]
    encoding_probes(tokenizer, template)
    return template, report, body
