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

d=get(['-n','ani-system','get','deployment','ani-resource-service'])
p=d['spec']['template']['spec']
selector=','.join(k+'='+v for k,v in sorted(d['spec']['selector']['matchLabels'].items()))
pods=get(['-n','ani-system','get','pods','-l',selector])['items']
result={'identity':actual,'deployment':{'name':d['metadata']['name'],'uid':d['metadata']['uid'],'generation':d['metadata']['generation'],'observed_generation':d.get('status',{}).get('observedGeneration'),'containers':[{'name':c['name'],'image':c['image'],'command':c.get('command'),'args':c.get('args'),'mounts':c.get('volumeMounts',[])} for c in p['containers']],'volumes':p.get('volumes',[])},'pods':[{'name':x['metadata']['name'],'uid':x['metadata']['uid'],'ip':x.get('status',{}).get('podIP'),'owners':x['metadata'].get('ownerReferences'),'containers':x.get('status',{}).get('containerStatuses',[])} for x in pods]}
print(json.dumps(result))
