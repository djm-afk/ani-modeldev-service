import base64,copy,datetime,getpass,hashlib,json,os,pathlib,re,socket,stat,subprocess,sys,urllib.parse
import yaml
CTX='kubernetes-admin@ani-kcn-full';ROOT=pathlib.Path('/home/kubercloud/tenantenv-20261101/gov-normal-input-import-244780-20261101');BINARY=pathlib.Path('/home/kubercloud/tenantenv-20261101/gov-normal-management-244780/admin');BIN_SHA='e058305ffbbcc2f77d204637d4b0b1f5e781bd9631e45fd8f7520911cbd7c1f9'
MD_UID='fc5ecff7-f2a7-4d1d-9a0e-6f2f1d4366a3';GOV_UID='4ae87828-5f2c-4b27-a8b8-d348aeb86ee5'
def require(ok,code):
 if not ok:raise RuntimeError(code)
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def sha(raw):return hashlib.sha256(raw).hexdigest()
def canonical(v):return json.dumps(v,sort_keys=True,separators=(',',':')).encode()
def k(*a):
 r=subprocess.run(['kubectl','--context',CTX,'--request-timeout=10s',*a,'-o','json'],capture_output=True,timeout=20);require(r.returncode==0,'EXACT_KUBE_READ_FAILED');return json.loads(r.stdout)
def identity():
 require(getpass.getuser()=='kubercloud' and socket.gethostname()=='node1' and os.environ.get('SSH_CONNECTION'),'SOLE_ENTRY_REQUIRED');c=k('config','view','--minify');require(c['current-context']==CTX and c['clusters'][0]['cluster']['server']=='https://lb.kubesphere.local:6443','CONTEXT_API_MISMATCH');require(k('get','namespace','kube-system')['metadata']['uid']=='3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','CLUSTER_UID_MISMATCH');s=k('-n','ani-system','get','secret','ani-resource-private');require(yaml.safe_load(base64.b64decode(s['data']['config.yaml']))['network']['cluster_id']=='f39bf6e2-f3ed-4191-ada3-caeebf86c220','CLUSTER_ID_MISMATCH');return {'ssh':'kubercloud@100.64.128.12','hostname':'node1','context':CTX,'apiserver':c['clusters'][0]['cluster']['server'],'cluster_id':'f39bf6e2-f3ed-4191-ada3-caeebf86c220','kube_system_uid':'3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','observed_at':now()}
def write(path,raw):
 fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
 with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
def regular(path,limit=4<<20):
 s=path.lstat();require(stat.S_ISREG(s.st_mode) and stat.S_IMODE(s.st_mode)==0o600 and s.st_size<=limit,'PRIVATE_FILE_SHAPE');fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW)
 with os.fdopen(fd,'rb') as f:
  require(os.fstat(f.fileno()).st_ino==s.st_ino,'PRIVATE_FILE_CHANGED');return f.read(limit+1)
def sources():
 d=k('-n','ani-system','get','deployment','ani-governance');require(d['metadata']['uid']==GOV_UID,'GOV_DEPLOYMENT_UID_CHANGED');co=next(x for x in d['spec']['template']['spec']['containers'] if x['name']=='governance');sec=k('-n','ani-system','get','secret','ani-governance-runtime');tls=k('-n','ani-system','get','secret','ani-governance-client-tls');ca=k('-n','ani-system','get','cm','ani-internal-service-ca')
 require(sec['metadata']['uid']=='ffc203e6-32a4-4876-8f8d-05bbe4351f72' and tls['metadata']['uid']=='8a449826-6123-4334-84fb-adcad61d543c' and ca['metadata']['uid']=='9587d616-9949-4306-b881-3e2fce0b55fa','ORIGINAL_AUTH_SOURCE_UID_CHANGED')
 # Exact current original projection chooses the client cert, not a same-name
 # stale field in another Secret. No service client certificate is generated.
 v=next(x for x in d['spec']['template']['spec']['volumes'] if x['name']=='runtime-input');items={}
 for src in v['projected']['sources']:
  for kind in ['secret','configMap']:
   if kind in src:
    for it in src[kind].get('items',[]):items[it['path']]=(kind,src[kind]['name'],it['key'])
 require(items.get('runtime.yaml')==('secret','ani-governance-runtime','runtime.yaml') and items.get('gov-client.crt')==('secret','ani-governance-client-tls','tls.crt') and items.get('gov-client.key')==('secret','ani-governance-client-tls','tls.key') and items.get('ca.crt')==('configMap','ani-internal-service-ca','ca.crt'),'ORIGINAL_PROJECTION_CHANGED')
 env={}
 for src in co.get('envFrom',[]):
  require(set(src)<= {'secretRef','prefix'} and 'secretRef' in src and not src.get('prefix'),'UNSUPPORTED_ORIGINAL_ENV_SOURCE');o=k('-n','ani-system','get','secret',src['secretRef']['name']);require(o['metadata']['name']=='ani-governance-environment' and o['metadata']['uid']=='59266531-0691-4e42-b7aa-dfc972716e9d','ORIGINAL_ENV_SOURCE_CHANGED')
  for key,val in o.get('data',{}).items():
   if key in ['GOWIND_CRYPTO_KEY','GWA_AUTH_JWT_PRIVATE_KEY','GWA_AUTH_JWT_PUBLIC_KEY','GWA_AUTH_JWT_KEY']:env[key]=base64.b64decode(val).decode()
 for e in co.get('env',[]):
  if e['name'].startswith('ANI_MODELDEV_') or e['name'] in ['GOWIND_CRYPTO_KEY','GWA_AUTH_JWT_PRIVATE_KEY','GWA_AUTH_JWT_PUBLIC_KEY','GWA_AUTH_JWT_KEY']:
   require('value' in e and 'valueFrom' not in e,'ORIGINAL_SELECTED_ENV_NOT_LITERAL');env[e['name']]=e['value']
 return d,sec,tls,ca,env
SERVICES=[('ani-platform','postgresql','a211aa89-9521-4570-836f-b9dbb1755e79','10.96.133.128',5432),('ani-platform','valkey','02aa5483-60f6-4be1-98d7-b05d8a6f6b40','10.96.137.255',6379),('ani-system','ani-modeldev','b9ba8e4b-3120-45be-8e77-109b866dd18d','10.96.180.137',9000)]
def service_proofs():
 out=[]
 for ns,name,uid,ip,port in SERVICES:
  o=k('-n',ns,'get','service',name);require(o['metadata']['uid']==uid and o['spec']['clusterIP']==ip and any(p['port']==port and p.get('protocol','TCP')=='TCP' for p in o['spec']['ports']),'ORIGINAL_SERVICE_BINDING_CHANGED');out.append({'namespace':ns,'name':name,'uid':uid,'resource_version':o['metadata']['resourceVersion'],'dial_vip':ip,'port':port})
 return out

EX='50dc7220-8001-463e-83f3-fe4d70f9af66';OP='ddd455b5-e6be-4d2a-abfe-e8ec64b8457f';TEN='64c5c35a-f554-432c-b4bc-7b4f36f08420'
def read(db,sql):
 pg=k('-n','ani-platform','get','pod','postgresql-0');require(pg['metadata']['uid']=='333fe87e-4a47-4e99-b5ea-33ef48be2e27','ORIGINAL_PG_UID_CHANGED')
 q=subprocess.run(['kubectl','--context',CTX,'--request-timeout=10s','-n','ani-platform','exec','-i','postgresql-0','--','sh','-c','PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=10000" exec psql -XqAt -U postgres -d '+db+' -v ON_ERROR_STOP=1'],input=('BEGIN READ ONLY; '+sql+' COMMIT;').encode(),capture_output=True,timeout=25);require(q.returncode==0,'ORIGINAL_B4_FACTS_READ_FAILED');return json.loads(q.stdout)
r={'schema':'ani.modeldev.a4-original-close-and-release.v1','observed_at':now(),'identity':identity(),'execution_id':EX,'operation_id':OP,'persistent_mutations':False}
r['modeldev']=read('ani_modeldev_v21',"SELECT jsonb_build_object('operation_id',operation_id,'spec_hash',spec_hash,'closed_at',closed_at,'close_generation',close_generation,'close_reason',close_reason,'close_requested_at',facts->'CloseRequestedAt','close_review_reason',facts->'CloseReviewReason','close_authority',facts->'CloseAuthority','owner_revision',facts->'OwnerRevision','writers_absent',facts->'Observation'->'WritersAbsent','workspace',jsonb_build_object('PVCName',facts->'Workspace'->'PVCName','PVCUID',facts->'Workspace'->'PVCUID'),'step_cleanup_completed_at',facts->'StepExitCleanup'->'CompletedAt','compute_cleanup',facts->'ComputeCleanup','gpu_release',facts->'GPURelease') FROM modeldev_execution_runtimes WHERE tenant_id='"+TEN+"' AND execution_id='"+EX+"';")
if r['modeldev'].get('compute_cleanup'):
 for kind in ['targets','requested','confirmed_absent']:
  for t in r['modeldev']['compute_cleanup'].get(kind,[]):t.pop('TrainingLog',None)
 r['modeldev']['compute_cleanup'].pop('logs',None)
r['governance']=read('ani_governance',"SELECT jsonb_build_object('acceptances',(SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'dispatch_state',dispatch_state,'stop_intent_generation',stop_intent_generation,'close_dispatch_state',close_dispatch_state,'lease_owner',lease_owner,'lease_until',lease_until,'close_lease_owner',close_lease_owner,'close_lease_until',close_lease_until)),'[]')FROM sys_modeldev_acceptances WHERE execution_id='"+EX+"' AND operation_id='"+OP+"'),'charge',(SELECT jsonb_build_object('charge_id',charge_id,'tenant_id',tenant_id,'operation_id',operation_id,'quota_code',quota_code,'original_units',original_units,'released_units',released_units,'remaining_units',original_units-released_units,'updated_at',updated_at) FROM sys_quota_charges WHERE operation_id='"+OP+"'),'account',(SELECT jsonb_build_object('id',a.id,'occupied_units',a.occupied_units,'version',a.version,'remaining_charge_sum',(SELECT COALESCE(sum(c.original_units-c.released_units),0)FROM sys_quota_charges c WHERE c.tenant_id=a.tenant_id AND c.quota_code=a.quota_code)) FROM sys_quota_accounts a WHERE (a.tenant_id,a.quota_code)=(SELECT tenant_id,quota_code FROM sys_quota_charges WHERE operation_id='"+OP+"')));")
r['finished_at']=now();print(json.dumps(r))
