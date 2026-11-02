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

import time
root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
name='modeldev-training-image-register-20261101';jobuid='8a24e3b6-4a8f-4157-a754-443dbdef5233';cmname=name+'-operator';cmuid='a124f8ef-b6cd-4938-8cf5-31e7c0a783b0'
j=get(['-n','ani-system','get','job',name]);c=get(['-n','ani-system','get','configmap',cmname]);assert j['metadata']['uid']==jobuid and c['metadata']['uid']==cmuid and j['status'].get('succeeded')==2
requests=[]
for path,uid,policy in [('/apis/batch/v1/namespaces/ani-system/jobs/'+name,jobuid,'Foreground'),('/api/v1/namespaces/ani-system/configmaps/'+cmname,cmuid,'Orphan')]:
 if 'configmaps/' in path:
  children=get(['-n','ani-system','get','pods','-l','batch.kubernetes.io/controller-uid='+jobuid])['items'];assert not children,'owned Job pods remain; retain operator ConfigMap'
 body=json.dumps({'apiVersion':'v1','kind':'DeleteOptions','preconditions':{'uid':uid},'propagationPolicy':policy}).encode()
 r=subprocess.run(['kubectl','--request-timeout=15s','delete','--raw',path,'-f','-'],input=body,capture_output=True,timeout=20);assert r.returncode==0,'exact task UID delete failed'
 requests.append({'path':path,'uid':uid,'exit':r.returncode})
 if 'jobs/' in path:
  deadline=time.monotonic()+30
  while True:
   children=get(['-n','ani-system','get','pods','-l','batch.kubernetes.io/controller-uid='+jobuid])['items']
   if not children:break
   assert time.monotonic()<deadline,'owned Job pods not yet absent'
   time.sleep(1)
absent=[]
for kind,n in [('job',name),('configmap',cmname)]:
 r=subprocess.run(['kubectl','--request-timeout=15s','-n','ani-system','get',kind,n,'--ignore-not-found','-o','json'],capture_output=True,timeout=20);assert r.returncode==0 and not r.stdout.strip();absent.append({'kind':kind,'name':n,'absent':True})
d=get(['-n','ani-system','get','deployment','ani-resource-service']);p=get(['-n','ani-system','get','pod','ani-resource-service-5d5bb7b9d9-6v4qb']);secret=get(['-n','ani-system','get','secret','ani-resource-private']);sa=get(['-n','ani-system','get','serviceaccount','ani-resource-service'])
assert d['metadata']['uid']=='9b17ed7e-6521-40e8-8bf5-70a44237e501' and d['metadata']['generation']==111
assert p['metadata']['uid']=='f6e8e746-1664-4544-8dca-ea05b53d7483'
assert secret['metadata']['uid']=='54303da4-f97a-451d-b186-4d1a35d0c466' and secret['metadata']['resourceVersion']=='20339931'
assert hashlib.sha256(base64.b64decode(secret['data']['config.yaml'])).hexdigest()=='87631172ea6104e430d302ef1885fbf0f8f11b6f929ef55bafcff87517ea15a3'
assert sa['metadata']['uid']=='b01307c9-f6e2-4122-b82a-dea120ed30f1'
receipt={'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'deleted_only_original_task_uids':requests,'absence':absent,'owned_job_pods_absent':True,'original_resource':{'deployment_uid':d['metadata']['uid'],'generation':d['metadata']['generation'],'pod_uid':p['metadata']['uid'],'secret_uid':secret['metadata']['uid'],'secret_resource_version':secret['metadata']['resourceVersion'],'config_sha256':'87631172ea6104e430d302ef1885fbf0f8f11b6f929ef55bafcff87517ea15a3','service_account_uid':sa['metadata']['uid'],'matched_before':True},'capability_existed_only_in_own_terminated_job_emptydirs':True}
(root/'training-register-job-cleanup.json').write_text(json.dumps(receipt,indent=2)+'\n');print(json.dumps(receipt))
