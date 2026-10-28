"""Internal export-only packaging seam; no extra public training parameter."""
import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('export_entry', Path(__file__).parents[1] / 'finetuning/finetune_entry.py')
ENTRY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ENTRY)


class ExportPackagingTest(unittest.TestCase):
    def test_controlled_packaging_preserves_frozen_export_settings(self):
        for size in (1, 3, 5):
            with patch.dict(os.environ, {'HF_HOME': '/workspace/work/export/hf'}):
                arguments = ENTRY.export_arguments(Path('/workspace'), Path('/workspace/export/merged'), export_size=size)
            self.assertEqual(arguments['export_size'], size)
            self.assertEqual(arguments['export_device'], 'cpu')
            self.assertEqual(arguments['infer_dtype'], 'float32')
            self.assertEqual(arguments['finetuning_type'], 'lora')
            self.assertFalse(arguments['trust_remote_code'])
            self.assertEqual(arguments['adapter_name_or_path'], '/workspace/training/adapter')

    def test_export_packaging_rejects_noninteger_or_nonpositive_values(self):
        for size in (True, False, 0, -1, 1.5, '1GB', None):
            with self.subTest(size=size), self.assertRaises(ValueError):
                ENTRY.export_arguments(Path('/workspace'), Path('/workspace/export/merged'), export_size=size)


if __name__ == '__main__':
    unittest.main()
