#!/usr/bin/env python3
import base64,hashlib,json,subprocess
from pathlib import Path
BASE=Path(__file__).resolve().parent
HELPER=BASE.parents[1]/'09/workbench-maintenance-job.py'
# Resolve package evidence/09 from this final-material subdirectory.
HELPER=BASE.parents[1]/'09/workbench-maintenance-job.py'
REMOTE=r'''
checked=identity()
from pathlib import Path
import ssl,urllib.request,urllib.error,hashlib,json,subprocess,base64,time
cm=kget('-n','ani-system','get','configmap','modeldev-trainimg-catalogue-20261030')
assert cm['metadata']['uid']=='55060a0b-e39c-4e86-b48e-6c7337099185'
keys=('builtin.mlp.cpu.json','builtin.mlp.gpu.json','builtin.sft_lora.cpu.json','builtin.sft_lora.gpu.json')
materials={}
for key in keys:
 raw=cm['data'][key].encode();m=json.loads(raw)
 assert set(m)<={'schema_version','implementation','program','training','workspace','component_resources','output_contract','finetuning'}
 materials[key]={'raw_base64':base64.b64encode(raw).decode(),'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
sc=kget('get','storageclass','ani-cephfs')
pvcs=kget('get','pvc','-A')['items']
cap=[{'namespace':x['metadata']['namespace'],'name':x['metadata']['name'],'uid':x['metadata']['uid'],'storage_class':x['spec'].get('storageClassName'),'access_modes':x['spec'].get('accessModes'),'phase':x.get('status',{}).get('phase'),'capacity':x.get('status',{}).get('capacity',{}).get('storage')} for x in pvcs if x['spec'].get('storageClassName')=='ani-cephfs' and x.get('status',{}).get('phase')=='Bound'][:8]
principal='system:serviceaccount:ani-system:modeldev-control'
sars=[]
for verb,name in [('create',''),('list','')]+[('get',name) for name in ('modeldev-mlp-cpu','modeldev-mlp-gpu','modeldev-sft-lora-cpu','modeldev-sft-lora-gpu')]:
 body={'apiVersion':'authorization.k8s.io/v1','kind':'SubjectAccessReview','spec':{'user':principal,'resourceAttributes':{'group':'pipelines.kubeflow.org','version':'v1beta1','resource':'pipelines','namespace':'ani-system','verb':verb,'name':name}}}
 p=subprocess.run(['kubectl','--context','kubernetes-admin@ani-kcn-full','create','-f','-','-o','json'],input=json.dumps(body).encode(),capture_output=True,timeout=20)
 assert p.returncode==0,('SAR unavailable',p.returncode)
 status=json.loads(p.stdout)['status'];sars.append({'attributes':body['spec']['resourceAttributes'],'status':{k:status[k] for k in ('allowed','denied','reason','evaluationError') if k in status}})
d=kget('-n','kubeflow','get','deployment','ml-pipeline')
c=next(c for c in d['spec']['template']['spec']['containers'] if c['name']=='ml-pipeline-api-server')
images={x['name']:x['value'] for x in c.get('env',[]) if x['name'] in ('V2_DRIVER_IMAGE','V2_LAUNCHER_IMAGE') and 'value' in x}
expected={'V2_DRIVER_IMAGE':'10.10.1.66:5001/ani-kubeflow/kubeflow/kfp-driver:2.16.0@sha256:249a0e2f2ce2ed078648c5bd3b90e61f202f4864d75376ba77853e81a53535cd','V2_LAUNCHER_IMAGE':'10.10.1.66:5001/ani-kubeflow/kubeflow/kfp-launcher:2.16.0@sha256:6b2d6c5733d957c9f7831cafe12aebcf9ef8ec503f57b74ed2ab00d5db113d29'}
assert images==expected,'Existing KFP support image source drift'
md=kget('-n','ani-system','get','deployment','ani-modeldev')
mdspec=md['spec']['template']['spec']
projected=[{'volume':v['name'],'path':x['serviceAccountToken'].get('path'),'audience':x['serviceAccountToken'].get('audience'),'expiration_seconds':x['serviceAccountToken'].get('expirationSeconds')} for v in mdspec.get('volumes',[]) for x in v.get('projected',{}).get('sources',[]) if 'serviceAccountToken' in x]
ca=kget('-n','kubeflow','get','configmap','ani-kfp-ca')['data']['ca.crt'].encode()
# Existing registry transport configuration only; no headers or credentials emitted.
hosts=[]
for path in ('/etc/containerd/certs.d/10.10.1.66:5001/hosts.toml','/etc/containerd/certs.d/_default/hosts.toml'):
 p=subprocess.run(['sudo','-n','test','-f',path],capture_output=True)
 if p.returncode==0:
  raw=subprocess.check_output(['sudo','-n','cat',path]);import tomllib
  cfg=tomllib.loads(raw.decode());hosts.append({'path':path,'sha256':hashlib.sha256(raw).hexdigest(),'server':cfg.get('server'),'hosts':{k:{'capabilities':v.get('capabilities'),'skip_verify':v.get('skip_verify'),'header_present':bool(v.get('header'))} for k,v in cfg.get('host',{}).items()}})
print(json.dumps({'identity':checked,'observed_at_unix':time.time(),'catalogue_source':{k:cm['metadata'][k] for k in ('name','namespace','uid','resourceVersion')},'materials':materials,'workspace_storage_class':{k:sc.get(k) for k in ('provisioner','volumeBindingMode','allowVolumeExpansion')},'workspace_capability_evidence':cap,'publication_sar':{'principal':principal,'groups_supplied':False,'reviews':sars},'support_images':images,'support_source':{k:d['metadata'][k] for k in ('name','namespace','uid','resourceVersion')},'original_md':{'uid':md['metadata']['uid'],'service_account':mdspec.get('serviceAccountName'),'projected_tokens':projected},'kfp_public_ca':{'sha256':hashlib.sha256(ca).hexdigest(),'base64':base64.b64encode(ca).decode()},'registry_transport_sources':hosts,'secrets_emitted':False}))
'''

def main():
 helper=BASE.parents[1]/'09/workbench-maintenance-job.py'
 # BASE=evidence/08/platform-materials-final; parents[1]=evidence.
 source="__name__='final_material_read_library'\n"+helper.read_text()+REMOTE
 r=subprocess.run(['ssh','-S','/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock','-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=source.encode(),capture_output=True,timeout=90)
 assert r.returncode==0,(r.returncode,r.stderr.decode())
 out=json.loads(r.stdout)
 originals=BASE/'original-materials';originals.mkdir(exist_ok=True)
 for name,m in out['materials'].items():
  raw=base64.b64decode(m.pop('raw_base64'));assert hashlib.sha256(raw).hexdigest()==m['sha256'];(originals/name).write_bytes(raw)
 raw=base64.b64decode(out['kfp_public_ca'].pop('base64'));(BASE/'kfp-ca.crt').write_bytes(raw)
 (BASE/'live-inputs.json').write_text(json.dumps(out,indent=2)+'\n')
 print(json.dumps({'identity':out['identity'],'materials':out['materials'],'publication_sar':out['publication_sar'],'workspace':out['workspace_capability_evidence'],'transport':out['registry_transport_sources']}))
if __name__=='__main__':main()
