#!/usr/bin/env python3
"""Prepare actual fixed-source IR remotely; missing publication/image IDs stay explicit."""
import base64,hashlib,json,subprocess
from pathlib import Path
BASE=Path(__file__).resolve().parent;WORK=BASE.parents[4]
FEDORA_ROOT='/home/chabking/tenantenv-20261101/modeldev-materials-d807-20261101'
SOURCE='d807cbdbd5dd6664d61480acedc752e19cfca48a';TREE='141053367fa4be9a6fb9c764b2d7900b5282be20'
files=('pipelines/cpu_p01.py','pipelines/requirements.txt','deploy/modeldev-environment/bundle.py','deploy/modeldev-environment/publisher.py','training/train_mlp.py','training/finetuning/finetune_entry.py','training/finetuning/configuration.py')
expected={f:hashlib.sha256(subprocess.check_output(['git','show',SOURCE+':'+f],cwd=WORK)).hexdigest() for f in files}
build=json.loads((BASE.parent/'build/build-inputs.json').read_text())
go_expected={k:v for k,v in build['input_files'].items() if k in ('go.mod','go.sum','scripts/modeldev-execution-materials/main.go') or (k.startswith('contract/cpup01/') and not k.endswith('_test.go') and '/conformance/' not in k)}
original={p.name:base64.b64encode(p.read_bytes()).decode() for p in (BASE/'original-materials').glob('*.json')}
inputs={'source_commit':SOURCE,'source_tree':TREE,'expected_python':expected,'expected_go':go_expected,'original_materials':original,'timing':json.loads((BASE/'original-timing-inputs.json').read_text()),'images':json.loads((BASE.parent/'image-publication/result.json').read_text())['images'],'anonymous_proof':json.loads((BASE/'anonymous-support-pull.json').read_text()),'anonymous_evidence_sha256':hashlib.sha256((BASE/'anonymous-support-pull.json').read_bytes()).hexdigest()}
remote=r'''
import json,sys,hashlib,shutil,os,subprocess,time,base64
from pathlib import Path
inputs=INPUTS;root=Path('/home/chabking/tenantenv-20261101/modeldev-materials-d807-20261101')
assert not root.exists(),'Preserve prior preparation; do not overwrite/recompile'
snapshot=Path('/home/chabking/tenantenv-20261101/modeldev-snapshot')
go_source=Path('/home/chabking/tenantenv-20261101/images-d807-cymq2rk8/source')
for name,sha in inputs['expected_python'].items():assert hashlib.sha256((snapshot/name).read_bytes()).hexdigest()==sha,('Python source drift',name)
for name,sha in inputs['expected_go'].items():assert hashlib.sha256((go_source/name).read_bytes()).hexdigest()==sha,('Go material renderer source drift',name)
venv='/home/chabking/workspace/modeldev-open-limits-20261028-01a182e4/kfp-resource-venv/bin/python'
p=subprocess.run([venv,'-c','import importlib.metadata as m,json;print(json.dumps({n:m.version(n) for n in ("kfp","kfp-kubernetes","kfp-pipeline-spec","kfp-server-api")}))'],capture_output=True,check=True)
versions=json.loads(p.stdout);assert all(x=='2.16.0' for x in versions.values())
root.mkdir(mode=0o700)
for name in inputs['expected_python']:
 p=root/'source'/name;p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(snapshot/name,p);p.chmod(0o444)
original=root/'original-materials';original.mkdir()
for name,b64 in inputs['original_materials'].items():p=original/name;p.write_bytes(base64.b64decode(b64));p.chmod(0o444)
timings=[v['timing'] for v in inputs['timing']['facts'].values()];assert len(timings)==2 and timings[0]==timings[1]
(root/'compile-configs').mkdir();(root/'pending-material-inputs').mkdir()
profiles=[];pending=[];mapping={'builtin.mlp.cpu.json':'modeldev-mlp-cpu','builtin.mlp.gpu.json':'modeldev-mlp-gpu','builtin.sft_lora.cpu.json':'modeldev-sft-lora-cpu','builtin.sft_lora.gpu.json':'modeldev-sft-lora-gpu'}
for name,profile in mapping.items():
 m=json.loads((original/name).read_text());implementation=m['implementation']['implementation_id'];mode='gpu' if profile.endswith('-gpu') else 'cpu';kind='GENERAL_TRAINING' if implementation=='builtin.mlp' else 'FINETUNING'
 config={'kind':kind,'component_image':inputs['images']['step']['immutable_image'],'owner_config_map':'modeldev-step-owner','storage_class':'ani-cephfs','workspace_size':'64Gi','workspace_access_mode':'ReadWriteMany','admission_profile':profile,'execution_timeout_max_seconds':timings[0]['execution_timeout_max_seconds'],'component_resources':m['component_resources']}
 path=root/'compile-configs'/(profile+'.json');path.write_text(json.dumps(config,sort_keys=True,separators=(',',':')));path.chmod(0o444);profiles.append({'id':profile,'compile_config':str(path)})
 entry='training/train_mlp.py' if kind=='GENERAL_TRAINING' else 'training/finetuning/finetune_entry.py'
 # This data is deliberately not a valid final facts schema: real IDs must be supplied before canonical rendering.
 input_data={'state':'AWAITING_REAL_PUBLICATION_AND_RESOURCE_IMAGE_REGISTRATION','schema_after_completion':'ani.modeldev.material-input.v3','implementation_id':implementation,'device_mode':mode,'training_image_known':{'image_scope':'platform','digest':inputs['images'][mode]['immutable_image'],'entry_sha256':hashlib.sha256((snapshot/entry).read_bytes()).hexdigest(),'python_executable':'/opt/conda/bin/python'},'pipeline_known':{},'workspace_storage_class':'ani-cephfs','workspace_capacity_bytes':68719476736,'component_resources':m['component_resources'],'missing':['training_image.image_id','training_image.image_version','pipeline.phase=CONFIRMED','pipeline.pipeline_id','pipeline.pipeline_version_id','pipeline.pipeline_ir_sha256'],'legacy_candidate_only':m.get('program',m.get('training'))}
 if kind=='FINETUNING':input_data['missing']+=['finetuning.runtime_capabilities measured from final image','finetuning.export_image and verify_image original registered final image identity'];input_data['mapping_sha256']=hashlib.sha256((snapshot/'training/finetuning/configuration.py').read_bytes()).hexdigest()
 out=root/'pending-material-inputs'/(profile+'.json');out.write_text(json.dumps(input_data,indent=2)+'\n');pending.append({'id':profile,'file':str(out),'missing':input_data['missing']})
anon=[{'reference':v['reference'],'evidence_sha256':inputs['anonymous_evidence_sha256']} for v in inputs['anonymous_proof']['images'].values()]
config={'source_commit':inputs['source_commit'],'source_tree':inputs['source_tree'],'registry_authority':'10.10.1.66:30003','platform_project':'ani-resource-factor10-20261022','anonymous_images':anon,'support_images':{'driver':inputs['anonymous_proof']['images']['driver']['reference'],'launcher':inputs['anonymous_proof']['images']['launcher']['reference'],'material_copy':inputs['images']['webhook']['immutable_image']},'profiles':profiles}
configpath=root/'bundle-input.json';configpath.write_text(json.dumps(config,sort_keys=True,separators=(',',':')))
cmd=[venv,str(root/'source/deploy/modeldev-environment/bundle.py'),'compile','--config',str(configpath),'--output',str(root/'prepared'),'--compiler',str(root/'source/pipelines/cpu_p01.py')]
started=time.monotonic();p=subprocess.run(cmd,capture_output=True);(root/'compile.stdout.log').write_bytes(p.stdout);(root/'compile.stderr.log').write_bytes(p.stderr)
receipt={'source_commit':inputs['source_commit'],'source_tree':inputs['source_tree'],'fixed_python_sha256':inputs['expected_python'],'fixed_go_renderer_inputs_sha256':inputs['expected_go'],'versions':versions,'compile_command':cmd,'compile_exit':p.returncode,'compile_elapsed_seconds':round(time.monotonic()-started,3),'workspace':{'storage_class':'ani-cephfs','capacity_bytes':68719476736,'size':'64Gi','access_mode':'ReadWriteMany','selection':'One platform value supports old MLP4Gi and SFT64Gi; live SC existing Bound64Gi/RWX observed; no legacy tenant binding copied'},'timing':timings[0],'timing_source':'Two original main ConfigMap facts; canonical material schema has no timing fields','pending_materials':pending,'publication':'BLOCKED_EXISTING_CONTROL_SAR_DENIED_NO_TOKEN_REQUEST_OR_POST','seal':'NOT_RUN_NO_CONFIRMED_PIPELINE_IDS_OR_REAL_NEW_IMAGE_IDS'}
if p.returncode==0:
 prepared=json.loads((root/'prepared/prepared.json').read_text());receipt['profiles']=prepared['profiles']
 # Build the existing strict canonical renderer, without installing anything globally.
 go='/home/chabking/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.linux-amd64/bin/go';assert hashlib.sha256(Path(go).read_bytes()).hexdigest()=='9c2a37ae5b21ba1d5a004bb9ab4d0c64de6fc5778e9d30487adccea62678b8b5'
 env={**os.environ,'CGO_ENABLED':'0','GOOS':'linux','GOARCH':'amd64','GOTOOLCHAIN':'auto'}
 command=[go,'build','-mod=readonly','-buildvcs=false','-trimpath','-o',str(root/'modeldev-execution-materials'),'./scripts/modeldev-execution-materials'];t=time.monotonic();g=subprocess.run(command,cwd=go_source,env=env,capture_output=True);(root/'renderer-build.stdout.log').write_bytes(g.stdout);(root/'renderer-build.stderr.log').write_bytes(g.stderr);receipt['canonical_renderer']={'command':command,'exit':g.returncode,'seconds':round(time.monotonic()-t,3),'source_directory':str(go_source)}
 if g.returncode==0:receipt['canonical_renderer'].update(sha256=hashlib.sha256((root/'modeldev-execution-materials').read_bytes()).hexdigest(),bytes=(root/'modeldev-execution-materials').stat().st_size)
receipt['status']='FOUR_FINAL_IR_COMPILED_PUBLICATION_AND_SEAL_BLOCKED' if p.returncode==0 else 'COMPILE_FAILED'
(root/'preparation-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')
print(json.dumps(receipt))
'''
payload='INPUTS='+repr(inputs)+'\n'+remote
r=subprocess.run(['ssh','-o','BatchMode=yes','fedora','python3','-'],input=payload.encode(),capture_output=True,timeout=300)
if r.returncode:
 out={'status':'REMOTE_PREPARATION_RESULT_UNKNOWN_OR_FAILED','ssh_exit':r.returncode,'stderr':r.stderr.decode()[-2500:]};(BASE/'preparation-start-failure.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out));raise SystemExit(1)
out=json.loads(r.stdout);(BASE/'preparation-receipt.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps({'status':out['status'],'compile_exit':out['compile_exit'],'profiles':[{k:p[k] for k in ('id','ir_sha256')} for p in out.get('profiles',[])],'renderer':out.get('canonical_renderer'),'fedora_root':FEDORA_ROOT}))
