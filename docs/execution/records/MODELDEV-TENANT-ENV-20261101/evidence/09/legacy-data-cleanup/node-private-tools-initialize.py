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
root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
if root.exists():assert not root.is_symlink() and stat.S_ISDIR(root.stat().st_mode) and stat.S_IMODE(root.stat().st_mode)==0o700
else:root.mkdir(mode=0o700,parents=True)
marker={'source_commit':'1464b131185791f71990b1cf864e1468186c8bd4','purpose':'Fixed readonly c99/native and exact DB cleanup tools; no live cleanup authorization in this receipt'}
mp=root/'transfer-source.json'
if mp.exists():assert json.loads(mp.read_text())==marker
else:mp.write_text(json.dumps(marker));mp.chmod(0o600)
print(json.dumps({'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'old_namespace_uid':ns['metadata']['uid'],'private_root':str(root),'source_marker':marker,'business_mutation':False}))
