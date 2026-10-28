"""Extract current capabilities from this final immutable image."""
import argparse,hashlib,importlib.metadata,json,platform,re,sys
from pathlib import Path

sys.path.insert(0,str(Path(__file__).resolve().parent))
sys.path.insert(0,str(Path(__file__).resolve().parent.parent))
from configuration import canonical
from template_runtime import PACKAGES,registered_template_sources

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--image-digest',required=True)
 args=parser.parse_args()
 if not re.fullmatch(r'[^\s@]+@sha256:[0-9a-f]{64}',args.image_digest):raise ValueError('full immutable image reference required')
 from transformers.models.auto.modeling_auto import MODEL_FOR_CAUSAL_LM_MAPPING_NAMES
 from llamafactory.data import template,converter
 import torch
 source=registered_template_sources(template)
 patches=json.loads(Path(__file__).with_name('runtime-patches').joinpath('official-source.json').read_bytes())
 actual=Path(converter.__file__).resolve()
 target=patches['patches'][0]['files'][0]
 if str(actual)!='/app/'+target['path'] or hashlib.sha256(actual.read_bytes()).hexdigest()!=target['after_sha256']:
  raise ValueError('actual converter import is not the fixed writer source')
 entry=Path(__file__).with_name('finetune_entry.py')
 caps={'schema_version':'ani.modeldev.finetuning-capabilities.v3','image_digest':args.image_digest,
       'entrypoint_sha256':hashlib.sha256(entry.read_bytes()).hexdigest(),
       'packages':[{'name':name,'version':importlib.metadata.version(name)} for name in sorted(PACKAGES)],
       'causal_lm_model_types':sorted(MODEL_FOR_CAUSAL_LM_MAPPING_NAMES),
       'registered_templates':[{'id':name,'source_sha256':source[name],'supports_system':True,'supports_multi_turn':True} for name in sorted(template.TEMPLATES)],
       'context_limit_fields':[{'source':'config.json','field':'max_position_embeddings'},{'source':'config.json','field':'n_positions'},{'source':'tokenizer_config.json','field':'model_max_length'}]}
 result={'capabilities':caps,'capabilities_sha256':hashlib.sha256(canonical(caps)).hexdigest(),
         'python_executable':sys.executable,'python_version':platform.python_version(),
         'torch_cuda_version':torch.version.cuda,'actual_converter_path':str(actual),
         'converter_sha256':target['after_sha256'],'entrypoint_path':str(entry)}
 print(json.dumps(result,sort_keys=True,indent=2))

if __name__=='__main__':main()
