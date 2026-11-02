#!/usr/bin/env python3
from pathlib import Path
import json,subprocess,hashlib
BASE=Path(__file__).resolve().parent
images=json.loads((BASE.parent/'image-publication/result.json').read_text())['images']
remote="__name__='final_runtime_inspect_library'\n"+(BASE.parents[1]/'09/workbench-maintenance-job.py').read_text()+"\nimages="+repr({k:images[k] for k in ('cpu','gpu')})+r'''
checked=identity()
import subprocess,json,hashlib,time,concurrent.futures
before=subprocess.check_output(['sudo','-n','ctr','-n','k8s.io','tasks','ls','-q']).decode().splitlines()
rows=subprocess.check_output(['sudo','-n','ctr','-n','k8s.io','images','ls']).decode().splitlines()
def inspect(mode):
 image=images[mode];matching=[line.split() for line in rows if line.split() and line.split()[0]==image['image']]
 assert len(matching)==1 and matching[0][2]==image['digest'],'Exact original exported image metadata mismatch'
 name='ani-md-d807-material-inspect-'+mode+'-20261101-8f6ba4'
 exists=subprocess.run(['sudo','-n','ctr','-n','k8s.io','containers','info',name],capture_output=True);assert exists.returncode!=0,'Private inspection container name already exists'
 command=['sudo','-n','ctr','-n','k8s.io','run','--rm','--read-only','--mount','type=tmpfs,dst=/tmp,options=rw:nosuid:nodev:mode=1777','--env','HF_HUB_OFFLINE=1','--env','TRANSFORMERS_OFFLINE=1','--env','HF_HOME=/tmp/hf','--env','MPLCONFIGDIR=/tmp/mpl',image['image'],name,'/opt/conda/bin/python','-I','/opt/ani-modeldev/training/finetuning/inspect_runtime.py','--image-digest',image['immutable_image']]
 start=time.monotonic();p=subprocess.run(command,capture_output=True,timeout=120)
 assert p.returncode==0,('Final image runtime introspection failed',mode,p.returncode,p.stderr.decode()[-1000:])
 result=json.loads(p.stdout);assert result['capabilities']['image_digest']==image['immutable_image']
 canonical=lambda x:json.dumps(x,sort_keys=True,separators=(',',':')).encode()
 assert hashlib.sha256(canonical(result['capabilities'])).hexdigest()==result['capabilities_sha256']
 absent=subprocess.run(['sudo','-n','ctr','-n','k8s.io','containers','info',name],capture_output=True);assert absent.returncode!=0
 return mode,{'image':image['immutable_image'],'metadata_tag':image['image'],'manifest_digest_verified':image['digest'],'command':command,'exit':p.returncode,'seconds':round(time.monotonic()-start,3),'stdout_sha256':hashlib.sha256(p.stdout).hexdigest(),'stderr_sha256':hashlib.sha256(p.stderr).hexdigest(),'actual':result,'container_absent_after':True,'training_performed':False,'gpu_requested':False,'host_business_mounts':False,'image_root_read_only':True,'temporary_writable_storage':'private tmpfs /tmp removed with ephemeral container'}
entries={}
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as ex:
 for f in concurrent.futures.as_completed([ex.submit(inspect,m) for m in ('cpu','gpu')]):
  k,v=f.result();entries[k]=v
after=subprocess.check_output(['sudo','-n','ctr','-n','k8s.io','tasks','ls','-q']).decode().splitlines()
print(json.dumps({'schema':'ani.modeldev.final-training-runtime-inspection.v1','identity':checked,'observed_at_unix':time.time(),'images':entries,'original_tasks_missing':sorted(set(before)-set(after)),'cluster_objects_created':False,'new_tools_installed':False}))
'''
r=subprocess.run(['ssh','-S','/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock','-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=remote.encode(),capture_output=True,timeout=150)
if r.returncode:
 out={'status':'RUNTIME_INSPECTION_NOT_CONFIRMED','ssh_exit':r.returncode,'stderr':r.stderr.decode()[-2500:]};(BASE/'runtime-inspection-failure.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out));raise SystemExit(1)
out=json.loads(r.stdout);(BASE/'final-runtime-capabilities.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps({'status':'FINAL_IMAGE_RUNTIME_CAPABILITIES_MEASURED','images':{k:{'exit':v['exit'],'seconds':v['seconds'],'entry_sha256':v['actual']['capabilities']['entrypoint_sha256'],'capabilities_sha256':v['actual']['capabilities_sha256'],'python':v['actual']['python_executable'],'templates':len(v['actual']['capabilities']['registered_templates']),'container_absent_after':v['container_absent_after']} for k,v in out['images'].items()},'original_tasks_missing':out['original_tasks_missing']}))
