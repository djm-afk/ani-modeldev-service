"""Current runtime boundaries reuse actual Go/Python context vectors."""
import importlib.util,json,os
from pathlib import Path
import tempfile,unittest,subprocess,sys
SPEC=importlib.util.spec_from_file_location('frozen_runtime_entry',Path(__file__).parents[1]/'finetuning/finetune_entry.py')
ENTRY=importlib.util.module_from_spec(SPEC);SPEC.loader.exec_module(ENTRY)
class RuntimeFrozenFactsTests(unittest.TestCase):
 def test_runtime_uses_same_context_vectors_as_actual_model_reader(self):
  vectors=json.loads((Path(__file__).parents[2]/'internal/data/finetuningmodel/testdata/context-limit-vectors.json').read_bytes())
  fields=[{'source':'config.json','field':'max_position_embeddings'},{'source':'tokenizer_config.json','field':'model_max_length'}]
  for vector in vectors:
   with self.subTest(vector['name']):
    objects={'config.json':{},'tokenizer_config.json':{}}
    if 'config' in vector:objects['config.json']['max_position_embeddings']=vector['config']
    if 'tokenizer' in vector:objects['tokenizer_config.json']['model_max_length']=vector['tokenizer']
    if 'error' in vector:
     with self.assertRaisesRegex(ValueError,vector['error']):ENTRY.resolve_context_limit(objects,fields)
    else:self.assertEqual(ENTRY.resolve_context_limit(objects,fields),vector['want'])
 def test_each_stage_uses_execution_owned_work_cache(self):
  original=dict(os.environ)
  try:
   with tempfile.TemporaryDirectory() as directory:
    root=Path(directory)
    for role in ('train','export','verify'):
     work=ENTRY.configure_work_directory(root,role)
     self.assertEqual(work,root/'work'/role)
     for name,suffix in [('TMPDIR','tmp'),('HF_HOME','hf'),('HF_DATASETS_CACHE','hf/datasets'),('MPLCONFIGDIR','matplotlib')]:
      self.assertEqual(os.environ[name],str(work/suffix));self.assertTrue((work/suffix).is_dir())
  finally:os.environ.clear();os.environ.update(original)
 def test_actual_cli_persists_finite_zero_optimizer_early_failure(self):
  with tempfile.TemporaryDirectory() as directory:
   root=Path(directory)
   p=subprocess.run([sys.executable,'-I',str(Path(__file__).parents[1]/'finetuning/finetune_entry.py'),'train',
      '--workspace',str(root),'--configuration',str(root/'missing-configuration.json'),
      '--dataset-manifest',str(root/'inputs/dataset-manifest.json'),
      '--expected-dataset-manifest-sha256','a'*64,'--expected-base-manifest-sha256','b'*64,
      '--expected-template-plan-sha256','c'*64],capture_output=True,text=True)
   self.assertNotEqual(p.returncode,0)
   failure=json.loads((root/'reports/train/failure.json').read_bytes())
   self.assertEqual(failure['optimizer_steps'],0)
   self.assertEqual(failure['phase'],'train')
   self.assertEqual(failure['reason'],'RUNTIME_EXECUTION_FAILED')
   self.assertFalse((root/'reports/train/summary.json').exists())
if __name__=='__main__':unittest.main()
