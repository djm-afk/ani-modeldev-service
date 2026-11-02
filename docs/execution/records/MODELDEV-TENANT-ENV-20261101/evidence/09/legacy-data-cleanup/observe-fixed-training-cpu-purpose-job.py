import base64,datetime,hashlib,json,os,re,stat,subprocess
from pathlib import Path
def run(args):
 p=subprocess.run(args,capture_output=True,timeout=25)
 if p.returncode:raise RuntimeError('fixed identity read failed')
 return p.stdout
def get(args):return json.loads(run(['kubectl','--request-timeout=15s']+args+['-o','json']))
context=run(['kubectl','config','current-context']).decode().strip()
api=get(['config','view','--minify'])['clusters'][0]['cluster']['server']
ks=get(['get','ns','kube-system'])['metadata']['uid']
s=get(['-n','ani-system','get','secret','ani-resource-private'])
m=re.findall(rb'(?m)^\s*cluster_id:\s*[\x22\x27]?([a-f0-9-]{36})',base64.b64decode(s['data']['config.yaml']))
assert len(m)==1
actual={'ssh':'kubercloud@100.64.128.12','context':context,'api_server':api,'cluster_id':m[0].decode(),'kube_system_uid':ks,'ssh_user':run(['id','-un']).decode().strip(),'hostname':run(['hostname']).decode().strip()}
expected={'ssh':'kubercloud@100.64.128.12','context':'kubernetes-admin@ani-kcn-full','api_server':'https://lb.kubesphere.local:6443','cluster_id':'f39bf6e2-f3ed-4191-ada3-caeebf86c220','kube_system_uid':'3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','ssh_user':'kubercloud','hostname':'node1'}
assert actual==expected,'full target identity mismatch'
ns=get(['get','ns','ani-modeldev-tenant-a-v21'])
assert ns['metadata']['uid']=='94f3afc0-7548-4099-9e11-3fa7fdf6cc67' and not ns['metadata'].get('deletionTimestamp')

requests=[{'kind': 'cpu', 'body': {'idempotency_key': 'modeldev-tenantenv-d807-20261101-cpu', 'image_reference': '10.10.1.66:30003/ani-resource-factor10-20261022/ani-modeldev-training-cpu@sha256:31227c3af9d3d0afe5cffddf34d6a3e0a922e29ea7e153892c8b0a0d1ed7f1d5', 'display_name': 'ModelDev Training CPU d807cbdb', 'description': 'Fixed ModelDev source d807cbdbd5dd6664d61480acedc752e19cfca48a; tenant environment implementation 20261101; immutable published digest.', 'purposes': ['training'], 'accelerator': 'none'}, 'body_byte_sha256': '54b820a14327dffff3159b5982f965b158907891f341d0e9e956a3f4abd96479', 'expected_digest': 'sha256:31227c3af9d3d0afe5cffddf34d6a3e0a922e29ea7e153892c8b0a0d1ed7f1d5'}, {'kind': 'gpu', 'body': {'idempotency_key': 'modeldev-tenantenv-d807-20261101-gpu', 'image_reference': '10.10.1.66:30003/ani-resource-factor10-20261022/ani-modeldev-training-gpu@sha256:aa1f6ae2159eead52d0d2343d53d785021aa4572dc9978e1af221eb30519eb6e', 'display_name': 'ModelDev Training GPU d807cbdb', 'description': 'Fixed ModelDev source d807cbdbd5dd6664d61480acedc752e19cfca48a; tenant environment implementation 20261101; immutable published digest.', 'purposes': ['training', 'finetuning'], 'accelerator': 'nvidia'}, 'body_byte_sha256': '82fd58776c400de83da6a5a739af1f68bc207919d53bbdf3d373f72c3d4fdac4', 'expected_digest': 'sha256:aa1f6ae2159eead52d0d2343d53d785021aa4572dc9978e1af221eb30519eb6e'}]
publication_result_byte_sha256='e35d2545ee9f54a9bd0849602f848f49cfec101612a141a92ff05b357e710205'

d=get(['-n','ani-system','get','deployment','ani-resource-service']);assert d['metadata']['uid']=='9b17ed7e-6521-40e8-8bf5-70a44237e501'
p=get(['-n','ani-system','get','pod','ani-resource-service-5d5bb7b9d9-6v4qb']);assert p['metadata']['uid']=='f6e8e746-1664-4544-8dca-ea05b53d7483' and p['spec']['nodeName']=='node3'
image='10.10.1.66:30003/ani-gpu-platform/ani-resource-service@sha256:71c04b8948728d8d0299320f148afa3fee3eabdbea87051c2b99c300abe730d1'
assert [c['image'] for c in p['spec']['containers'] if c['name']=='resource']==[image]
sa=get(['-n','ani-system','get','serviceaccount',p['spec']['serviceAccountName']]);assert sa['metadata']['name']=='ani-resource-service' and sa['metadata']['uid']=='b01307c9-f6e2-4122-b82a-dea120ed30f1'
assert s['metadata']['uid']=='54303da4-f97a-451d-b186-4d1a35d0c466' and s['metadata']['resourceVersion']=='20339931'
configsha=hashlib.sha256(base64.b64decode(s['data']['config.yaml'])).hexdigest();assert configsha=='87631172ea6104e430d302ef1885fbf0f8f11b6f929ef55bafcff87517ea15a3'
name='modeldev-training-cpu-purpose-20261101';cmname=name+'-operator'

root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
raw=(root/'training-image-cpu-purpose-job-create.stdout').read_text();decoder=json.JSONDecoder();created=[]
while raw.strip():
 value,end=decoder.raw_decode(raw.lstrip());created.append(value);raw=raw.lstrip()[end:]
assert len(created)==2 and {x['kind'] for x in created}=={'ConfigMap','Job'}
objects={o['kind']:o for o in created}
job=get(['-n','ani-system','get','job',name]);cm=get(['-n','ani-system','get','configmap',cmname])
assert job['metadata']['uid']==objects['Job']['metadata']['uid'] and cm['metadata']['uid']==objects['ConfigMap']['metadata']['uid']
pods=get(['-n','ani-system','get','pods','-l','batch.kubernetes.io/job-name='+name])['items']
results=[]
for child in pods:
 assert any(o['uid']==job['metadata']['uid'] and o.get('controller') for o in child['metadata'].get('ownerReferences',[]))
 item={'name':child['metadata']['name'],'uid':child['metadata']['uid'],'index':child['metadata'].get('annotations',{}).get('batch.kubernetes.io/job-completion-index'),'phase':child['status'].get('phase'),'init_status':child['status'].get('initContainerStatuses',[]),'container_status':child['status'].get('containerStatuses',[])}
 r=subprocess.run(['kubectl','--request-timeout=15s','-n','ani-system','logs',child['metadata']['name'],'-c','register-platform','--limit-bytes=1048576'],capture_output=True,timeout=20)
 if r.returncode==0:
  path=root/('training-register-'+item['index']+'.log');path.write_bytes(r.stdout);path.chmod(0o600)
  item['log_sha256']=hashlib.sha256(r.stdout).hexdigest();item['receipts']=[]
  for line in r.stdout.splitlines():
   try:v=json.loads(line)
   except Exception:continue
   if isinstance(v,dict) and v.get('action')=='update-platform':item['receipts'].append(v)
 else:item['log_read_exit']=r.returncode
 results.append(item)
receipt={'schema':'ani.modeldev.training-image-registration-job-observation.v1','observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'created':[{'kind':x['kind'],'name':x['metadata']['name'],'uid':x['metadata']['uid']} for x in created],'job_status':job.get('status'),'pods':results,'requests':requests,'no_job_recreated':True,'no_new_rbac_or_service_account':True}
(root/'training-image-cpu-purpose-job-observation.json').write_text(json.dumps(receipt,indent=2)+'\n');(root/'training-image-cpu-purpose-job-observation.json').chmod(0o600)
print(json.dumps(receipt))
