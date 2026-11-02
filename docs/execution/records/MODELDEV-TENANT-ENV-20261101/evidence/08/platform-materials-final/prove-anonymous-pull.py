#!/usr/bin/env python3
from pathlib import Path
import json,subprocess,hashlib
BASE=Path(__file__).resolve().parent
REMOTE=r'''
checked=identity()
import urllib.request,urllib.error,ssl,subprocess,hashlib,json,time,tomllib
expected={'driver':'10.10.1.66:5001/ani-kubeflow/kubeflow/kfp-driver:2.16.0@sha256:249a0e2f2ce2ed078648c5bd3b90e61f202f4864d75376ba77853e81a53535cd','launcher':'10.10.1.66:5001/ani-kubeflow/kubeflow/kfp-launcher:2.16.0@sha256:6b2d6c5733d957c9f7831cafe12aebcf9ef8ec503f57b74ed2ab00d5db113d29'}
raw=subprocess.check_output(['sudo','-n','cat','/etc/containerd/config.toml']);cfg=tomllib.loads(raw.decode());selected=[]
def walk(x,path=()):
 if isinstance(x,dict):
  for k,v in x.items():
   if k=='10.10.1.66:5001' and isinstance(v,dict):selected.append({'path':'.'.join(path+(k,)),'endpoint':v.get('endpoint'),'auth_present':bool(v.get('auth')),'tls_keys':sorted(v.get('tls',{}))})
   walk(v,path+(k,))
walk(cfg)
# Require explicit existing transport, not a new insecure registry setting.
assert any(x.get('endpoint') and all(y.startswith('http://10.10.1.66:5001') for y in x['endpoint']) for x in selected),'Original registry HTTP endpoint not established; do not guess transport'
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
proof={}
for name,ref in expected.items():
 authority_repo,digest=ref.split('@');repo=authority_repo.split('/',1)[1].rsplit(':',1)[0]
 url='http://10.10.1.66:5001/v2/'+repo+'/manifests/'+digest
 req=urllib.request.Request(url,headers={'Accept':'application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'})
 with opener.open(req,timeout=10) as response:
  assert response.status==200 and response.url==url
  body=response.read(1048577);assert len(body)<=1048576 and 'sha256:'+hashlib.sha256(body).hexdigest()==digest
  manifest=json.loads(body)
 started=time.monotonic();p=subprocess.run(['sudo','-n','ctr','-n','k8s.io','images','pull','--plain-http','--platform','linux/amd64',ref],capture_output=True,timeout=90)
 assert p.returncode==0,('Anonymous pull failed',name,p.returncode,hashlib.sha256(p.stderr).hexdigest())
 physical=[]
 for d in manifest['layers']+[manifest['config']]:
  blob='/var/lib/containerd/io.containerd.content.v1.content/blobs/sha256/'+d['digest'].split(':',1)[1]
  size=int(subprocess.check_output(['sudo','-n','stat','-c','%s',blob]));assert size==d['size'];physical.append({'digest':d['digest'],'size':size})
 cfgraw=subprocess.check_output(['sudo','-n','ctr','-n','k8s.io','content','get',manifest['config']['digest']]);assert 'sha256:'+hashlib.sha256(cfgraw).hexdigest()==manifest['config']['digest'];imagecfg=json.loads(cfgraw);assert imagecfg['os']=='linux' and imagecfg['architecture']=='amd64'
 proof[name]={'reference':ref,'anonymous_manifest_http_status':200,'manifest_sha256':digest,'manifest':manifest,'platform':{'os':imagecfg['os'],'architecture':imagecfg['architecture']},'pull_command':['sudo','-n','ctr','-n','k8s.io','images','pull','--plain-http','--platform','linux/amd64',ref],'pull_exit':p.returncode,'pull_elapsed_seconds':round(time.monotonic()-started,3),'pull_stdout_sha256':hashlib.sha256(p.stdout).hexdigest(),'pull_stderr_sha256':hashlib.sha256(p.stderr).hexdigest(),'all_layer_config_bytes_present':physical,'credentials_supplied':False,'authorization_header':False,'skip_tls_verify_used':False,'transport':'Exact pre-existing HTTP registry endpoint5001, not managed Harbor30003 TLS authority'}
print(json.dumps({'schema':'ani.modeldev.anonymous-support-images.v1','identity':checked,'observed_at_unix':time.time(),'original_containerd_config_sha256':hashlib.sha256(raw).hexdigest(),'original_registry_transport':selected,'images':proof,'shared_containerd_configuration_modified':False,'new_identity_or_credentials':False}))
'''
source="__name__='anonymous_pull_library'\n"+(BASE.parents[1]/'09/workbench-maintenance-job.py').read_text()+REMOTE
r=subprocess.run(['ssh','-S','/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock','-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=source.encode(),capture_output=True,timeout=210)
if r.returncode:
 out={'status':'ANONYMOUS_PULL_NOT_PROVEN','ssh_exit':r.returncode,'stderr':r.stderr.decode()[-2500:]};(BASE/'anonymous-support-pull-blocked.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out));raise SystemExit(1)
raw=r.stdout;out=json.loads(raw);(BASE/'anonymous-support-pull.json').write_text(json.dumps(out,sort_keys=True,separators=(',',':')))
print(json.dumps({'status':'ANONYMOUS_PULL_PROVEN','images':{k:{'reference':v['reference'],'pull_exit':v['pull_exit'],'seconds':v['pull_elapsed_seconds'],'layer_count':len(v['manifest']['layers'])} for k,v in out['images'].items()},'evidence_sha256':hashlib.sha256((BASE/'anonymous-support-pull.json').read_bytes()).hexdigest()}))
