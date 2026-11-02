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

import urllib.parse
source=base64.b64decode(s['data']['config.yaml']).decode()
section=re.search(r'(?ms)^image:\s*\n(.*?)(?=^\S|\Z)',source).group(1)
def selected(key):
 m=re.findall(r'(?m)^\s+'+key+r':\s*[\x22\x27]?([^\x22\x27\n]+)',section)
 assert len(m)==1,'fixed image owner config missing'
 return m[0].strip()
assert selected('installation_id')=='7e1334f4-5202-4819-8d9d-96248d3d4b00'
assert selected('platform_project')=='ani-resource-factor10-20261022'
assert selected('harbor_url').rstrip('/')=='https://10.10.1.66:30003'
dsn_key=Path(selected('database_dsn_file')).name
assert dsn_key=='image-runtime.dsn'
dsn=base64.b64decode(s['data'][dsn_key]).decode().strip()
db=urllib.parse.unquote(urllib.parse.urlsplit(dsn).path.lstrip('/'))
assert re.fullmatch('[a-zA-Z0-9_]+',db)
query="""SELECT json_build_object('platform_spaces',(SELECT coalesce(json_agg(row_to_json(p)),'[]') FROM (SELECT space_id,owner_scope,installation_id,registry_authority,project_name,harbor_project_id,state,version FROM image.spaces WHERE owner_scope='platform' AND tenant_id IS NULL AND installation_id='7e1334f4-5202-4819-8d9d-96248d3d4b00')p),'registrations',(SELECT coalesce(json_agg(row_to_json(r)),'[]') FROM (SELECT image_id,owner_scope,tenant_id,space_id,repository,source_reference,digest,platforms,purposes,accelerator,version,created_by,updated_by,unregistered_at FROM image.registrations WHERE owner_scope='platform' AND tenant_id IS NULL AND unregistered_at IS NULL AND digest IN ('sha256:31227c3af9d3d0afe5cffddf34d6a3e0a922e29ea7e153892c8b0a0d1ed7f1d5','sha256:aa1f6ae2159eead52d0d2343d53d785021aa4572dc9978e1af221eb30519eb6e') ORDER BY image_id)r))"""
args=['kubectl','--request-timeout=15s','-n','ani-platform','exec','-i','postgresql-0','--','sh','-c','PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=15000" exec psql -U postgres -d '+db+' -At -v ON_ERROR_STOP=1']
r=subprocess.run(args,input=query.encode(),capture_output=True,timeout=25)
assert r.returncode==0,'fixed read-only catalog inspection failed'
v=json.loads(r.stdout)
assert len(v['platform_spaces'])==1 and v['platform_spaces'][0]['state']=='available' and v['platform_spaces'][0]['project_name']=='ani-resource-factor10-20261022' and v['platform_spaces'][0]['registry_authority']=='10.10.1.66:30003'
print(json.dumps({'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'readonly':True,'image_database':db,'original_operator_config':{'installation_id':selected('installation_id'),'platform_project':selected('platform_project'),'harbor_url':selected('harbor_url'),'database_dsn_file':selected('database_dsn_file')},'catalog':v}))
