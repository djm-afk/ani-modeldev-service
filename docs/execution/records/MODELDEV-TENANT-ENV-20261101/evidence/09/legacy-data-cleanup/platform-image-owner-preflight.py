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

source=base64.b64decode(s['data']['config.yaml']).decode()
match=re.search(r'(?ms)^image:\s*\n(.*?)(?=^\S|\Z)',source)
assert match,'image section unavailable'
section=match.group(1)
keys=['installation_id','registry_authority','platform_project','platform_project_name','database_url_file','platform_actor','harbor_password_file','harbor_ca_file']
config={}
for key in keys:
 found=re.findall(r'(?m)^\s+'+key+r':\s*[\x22\x27]?([^\x22\x27\n]+)',section)
 if found:config[key]=found[0].strip()
assert config.get('registry_authority')=='10.10.1.66:30003'
d=get(['-n','ani-system','get','deployment','ani-resource-service'])
pods=get(['-n','ani-system','get','pods','-l',','.join(k+'='+v for k,v in d['spec']['selector']['matchLabels'].items())])['items']
assert len(pods)==1
p=pods[0]
assert p['metadata']['uid']=='f6e8e746-1664-4544-8dca-ea05b53d7483'
help_result=subprocess.run(['kubectl','--request-timeout=15s','-n','ani-system','exec',p['metadata']['name'],'-c','resource','--','/ani-resource-service','-help'],capture_output=True,timeout=25)
flags=[line.strip() for line in (help_result.stdout+help_result.stderr).decode().splitlines() if 'image-admin' in line or 'image-operator' in line]
print(json.dumps({'identity':actual,'resource_pod':{'name':p['metadata']['name'],'uid':p['metadata']['uid']},'image_config_nonsecret_references':config,'existing_binary_help_exit':help_result.returncode,'operator_flags':flags}))
