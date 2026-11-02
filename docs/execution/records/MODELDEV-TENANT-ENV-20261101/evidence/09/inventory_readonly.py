import base64,datetime,hashlib,json,subprocess

def run(args):
 r=subprocess.run(args,capture_output=True,timeout=45)
 if r.returncode:raise RuntimeError('read operation failed: '+args[0])
 return r.stdout

def kget(*args):return json.loads(run(['kubectl',*args,'-o','json']))

def sql(db,query):
 args=['kubectl','-n','ani-platform','exec','-i','postgresql-0','--','sh','-c','PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=15000" exec psql -U postgres -d '+db+' -At -v ON_ERROR_STOP=1']
 r=subprocess.run(args,input=query.encode(),capture_output=True,timeout=40)
 if r.returncode:raise RuntimeError('read-only SQL failed for '+db)
 return json.loads(r.stdout)

context=run(['kubectl','config','current-context']).decode().strip()
api=json.loads(run(['kubectl','config','view','--minify','-o','json']))['clusters'][0]['cluster']['server']
ks=kget('get','ns','kube-system')['metadata']['uid']
# Select only the non-sensitive cluster ID from the private resource config.
s=kget('-n','ani-system','get','secret','ani-resource-private')
raw=base64.b64decode(s['data']['config.yaml']).decode()
import re
m=re.search(r'^\s*cluster_id:\s*["\']?([a-f0-9-]{36})',raw,re.M)
cluster=m.group(1) if m else None
expected=('kubernetes-admin@ani-kcn-full','https://lb.kubesphere.local:6443','3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','f39bf6e2-f3ed-4191-ada3-caeebf86c220')
if (context,api,ks,cluster)!=expected:raise RuntimeError('full target identity mismatch')
report={'schema':'ani.modeldev.retirement-inventory.v1','read_only':True,'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'target':dict(zip(['context','apiserver','kube_system_uid','cluster_id'],expected)),'ssh':'kubercloud@100.64.128.12'}
report['schema_columns']=sql('ani_modeldev_v21',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT table_name,column_name,data_type FROM information_schema.columns WHERE table_schema='public' AND table_name LIKE 'modeldev_%' ORDER BY table_name,ordinal_position)x")
report['foreign_keys']=sql('ani_modeldev_v21',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT conname,conrelid::regclass::text AS child,confrelid::regclass::text AS parent,pg_get_constraintdef(oid) AS definition FROM pg_constraint WHERE contype='f' AND connamespace='public'::regnamespace ORDER BY conname)x")
report['triggers']=sql('ani_modeldev_v21',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT tgname,tgrelid::regclass::text AS table_name,pg_get_triggerdef(oid) AS definition FROM pg_trigger WHERE NOT tgisinternal ORDER BY tgname)x")
rows=sql('ani_modeldev_v21',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT a.tenant_id,a.execution_id,a.operation_id,a.spec_hash,a.accepted_at,encode(a.snapshot_canonical,'base64') AS snapshot,i.close_generation,i.owner_revision,r.facts,r.closed_at,r.gpu_release_sealed_at,r.gpu_release_scope_sha256 FROM modeldev_executions a JOIN modeldev_execution_identities i USING(tenant_id,execution_id) LEFT JOIN modeldev_execution_runtimes r USING(tenant_id,execution_id) ORDER BY a.tenant_id,a.execution_id)x")
for row in rows:
 raw=base64.b64decode(row.pop('snapshot'));s=json.loads(raw);row['snapshot_sha256']=hashlib.sha256(raw).hexdigest();row['schema_version']=s.get('schema_version');row['kind']=s.get('kind');row['environment']=s.get('environment');row['implementation']=s.get('implementation');row['input']=s.get('input');row['publication_scope']=s.get('publication_scope');row['compute_origin']=s.get('compute_origin')
 if s.get('finetuning'):row['base_model']=s['finetuning'].get('base_model')
 facts=row.pop('facts') or {}
 # Runtime receipts contain object identities and no raw workload logs.
 row['runtime']={k:facts[k] for k in ['Workspace','Training','TrainingHandle','Observation','CloseEvidence','CloseAuthority','CloseReviewReason','ComputeCleanup','StepExitCleanup','close_operation_id','GPURelease','RuntimeTemplate'] if k in facts}
for row in rows:
 if isinstance(row['runtime'].get('Training'),dict):row['runtime']['Training'].pop('Snapshot',None)
report['executions']=rows
for key,table in [('dispatches','modeldev_pipeline_dispatches'),('runs','modeldev_pipeline_confirmed_runs'),('authorities','modeldev_run_authorities'),('inputs','modeldev_input_versions'),('cleanup_audits','modeldev_execution_cleanup_audits'),('close_intents','modeldev_close_intents')]:
 cols='*' if key!='dispatches' else 'tenant_id,execution_id,operation_id,spec_hash,attempt_id,plan_hash,state,reserved_at,uncertain_at,not_sent_at'
 report[key]=sql('ani_modeldev_v21',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT "+cols+' FROM '+table+' ORDER BY tenant_id)x')
report['gov_schema_columns']=sql('ani_governance',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT table_name,column_name,data_type FROM information_schema.columns WHERE table_schema='public' AND (table_name LIKE 'sys_modeldev%' OR table_name LIKE 'sys_quota%') ORDER BY table_name,ordinal_position)x")
report['gov_foreign_keys']=sql('ani_governance',"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT conname,conrelid::regclass::text AS child,confrelid::regclass::text AS parent,pg_get_constraintdef(oid) AS definition FROM pg_constraint WHERE contype='f' AND (conrelid::regclass::text LIKE 'sys_modeldev%' OR conrelid::regclass::text LIKE 'sys_quota%') ORDER BY conname)x")
def summarize(value):
 if isinstance(value,dict):
  if isinstance(value.get('Lines'),list):
   raw=json.dumps(value.pop('Lines'),ensure_ascii=False,separators=(',',':')).encode()
   value['retained_lines_evidence']={'canonical_json_sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw),'contents_omitted':True}
  for child in value.values():summarize(child)
 elif isinstance(value,list):
  for child in value:summarize(child)
summarize(report)
print(json.dumps(report,ensure_ascii=False,indent=2))
