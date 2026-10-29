"""Exercise native template EOS facts with the pinned Transformers tokenizer API."""
import copy
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'finetuning'))
from template_runtime import (RuntimeFailure, assert_effective_tokenizer,
                              registered_template_sources, resolve_effective_template,
                              token_facts)


class TemplateEOSFactsTests(unittest.TestCase):
    def tokenizer(self):
        from tokenizers import Tokenizer, models, pre_tokenizers
        from transformers import PreTrainedTokenizerFast

        backend = Tokenizer(models.WordLevel({
            '<|endoftext|>': 0, '<|im_start|>': 1, '<|im_end|>': 2,
            'ordinary': 3,
        }, unk_token='<|endoftext|>'))
        backend.pre_tokenizer = pre_tokenizers.Whitespace()
        return PreTrainedTokenizerFast(tokenizer_object=backend,
            unk_token='<|endoftext|>', bos_token='<|endoftext|>',
            eos_token='<|endoftext|>', pad_token='<|endoftext|>')

    def test_named_native_stop_token_survives_formal_resolve_and_reload(self):
        from llamafactory.data import template as native
        from llamafactory.hparams import DataArguments
        from transformers import AutoTokenizer

        tokenizer = self.tokenizer()
        plan = {'mode': 'NAMED', 'template_id': 'smollm2',
                'source_sha256': registered_template_sources(native)['smollm2'],
                'runtime_capabilities_sha256': '0' * 64, 'image_digest': 'unit-test'}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            template, report, body = resolve_effective_template(
                root, plan, tokenizer, DataArguments(template='smollm2'),
                lambda *_: self.fail('named template must use the native registry'))
            self.assertEqual(report['eos_ids'], [0, 2])
            self.assertNotIn('<|im_end|>', tokenizer.special_tokens_map.values())
            self.assertIn('<|im_end|>', tokenizer.extra_special_tokens)
            self.assertTrue(tokenizer.added_tokens_decoder[2].special)
            self.assertIn(('extra_special_tokens.0', '<|im_end|>', '2'),
                          [(v['name'], v['token'], v['id']) for v in report['special_tokens']])
            tokenizer.save_pretrained(root / 'saved')
            reloaded = AutoTokenizer.from_pretrained(root / 'saved', local_files_only=True)
            assert_effective_tokenizer(reloaded, report, body)
            parsed = native.parse_template(reloaded)
            parsed.stop_words = [reloaded.convert_ids_to_tokens(i)
                                 for i in report['eos_ids'] if i != reloaded.eos_token_id]
            self.assertEqual(token_facts(reloaded, parsed), token_facts(tokenizer, template))

    def test_ordinary_vocabulary_stop_token_is_rejected(self):
        from llamafactory.data import template as native
        from llamafactory.hparams import DataArguments

        tokenizer = self.tokenizer()
        template = native.get_template_and_fix_tokenizer(tokenizer, DataArguments(template='smollm2'))
        template = copy.deepcopy(template)
        template.stop_words = ['ordinary']
        with self.assertRaisesRegex(RuntimeFailure, 'EOS facts are outside special-token vocabulary'):
            token_facts(tokenizer, template)

    def test_unmapped_native_stop_token_is_rejected(self):
        from llamafactory.data import template as native
        from llamafactory.hparams import DataArguments

        tokenizer = self.tokenizer()
        template = copy.deepcopy(native.get_template_and_fix_tokenizer(
            tokenizer, DataArguments(template='smollm2')))
        template.stop_words = ['missing-stop-token']
        with self.assertRaises(RuntimeFailure):
            token_facts(tokenizer, template)

    def test_invalid_stop_id_boundary_is_rejected(self):
        for identifier in (None, True, '2', -1, 999999):
            with self.subTest(identifier=identifier):
                tokenizer = self.tokenizer()
                template = SimpleNamespace(get_stop_token_ids=lambda _: [0, identifier], stop_words=[])
                with self.assertRaises(RuntimeFailure):
                    token_facts(tokenizer, template)


if __name__ == '__main__':
    unittest.main()
