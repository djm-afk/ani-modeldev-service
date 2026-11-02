"""One bounded, exact-five read; canonical bytes go only to private Fedora input."""
import datetime,hashlib,json,subprocess,sys
from pathlib import Path

HERE=Path(__file__).resolve().parent
ENTRY='kubercloud@100.64.128.12'
IDS=['f2168fbf-d7c9-4e96-8cc6-784bb1546083','27cf73a3-4107-4c07-86f7-862ce9fb413d','c9b470e0-d9cb-4989-a7f2-d1fbfbe72ab9','91e351e7-102e-4807-b0d2-9236c1426da9','f148135c-640f-47ed-b9b1-4b039d4709d1']
REMOTE=r'''
import base64,datetime,getpass,hashlib,json,re,socket,subprocess
IDS=['f2168fbf-d7c9-4e96-8cc6-784bb1546083','27cf73a3-4107-4c07-86f7-862ce9fb413d','c9b470e0-d9cb-4989-a7f2-d1fbfbe72ab9','91e351e7-102e-4807-b0d2-9236c1426da9','f148135c-640f-47ed-b9b1-4b039d4709d1']
def run(args,body=None):
 p=subprocess.run(args,input=body,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=40)
 if p.returncode:raise RuntimeError('FIXED_READ_FAILED')
 return p.stdout
def kube(*args):return json.loads(run(['kubectl','--request-timeout=15s',*args,'-o','json']))
context=run(['kubectl','config','current-context']).decode().strip()
view=json.loads(run(['kubectl','config','view','--minify','-o','json']))
if len(view['clusters'])!=1:raise RuntimeError('CLUSTER_VIEW_DIFFERS')
api=view['clusters'][0]['cluster']['server'];ks=kube('get','ns','kube-system')['metadata']['uid']
secret=kube('-n','ani-system','get','secret','ani-resource-private')
private=base64.b64decode(secret['data']['config.yaml']).decode()
clusters=re.findall(r'^\s*cluster_id:\s*["\']?([a-f0-9-]{36})',private,re.M)
if len(clusters)!=1:raise RuntimeError('CLUSTER_ID_DIFFERS')
identity={'ssh':'kubercloud@100.64.128.12','ssh_user':getpass.getuser(),'hostname':socket.gethostname(),'context':context,'apiserver':api,'kube_system_uid':ks,'cluster_id':clusters[0]}
if (identity['ssh_user'],context,api,ks,clusters[0])!=('kubercloud','kubernetes-admin@ani-kcn-full','https://lb.kubesphere.local:6443','3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','f39bf6e2-f3ed-4191-ada3-caeebf86c220'):raise RuntimeError('FULL_IDENTITY_DIFFERS')
def sql(database,query):
 command=['kubectl','--request-timeout=30s','-n','ani-platform','exec','-i','postgresql-0','--','sh','-c','PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=15000" exec psql -qAt -U postgres -d '+database+' -v ON_ERROR_STOP=1']
 return json.loads(run(command,query.encode()))
selection=','.join("'"+v+"'" for v in IDS)
md_tables=['modeldev_executions','modeldev_execution_identities','modeldev_execution_runtimes','modeldev_pipeline_dispatches','modeldev_pipeline_confirmed_runs','modeldev_run_authorities','modeldev_close_intents','modeldev_execution_cleanup_audits']
parts=["'database',current_database()","'database_user',current_user","'transaction_read_only',current_setting('transaction_read_only')","'observed_at',clock_timestamp()"]
for table in md_tables:parts.append("'"+table+"',(SELECT coalesce(json_agg(to_jsonb(t)),'[]') FROM "+table+" t WHERE tenant_id='64c5c35a-f554-432c-b4bc-7b4f36f08420'::uuid AND execution_id IN ("+selection+"))")
md=sql('ani_modeldev_v21','SELECT json_build_object('+','.join(parts)+')')
gov=sql('ani_governance',"SELECT json_build_object('database',current_database(),'database_user',current_user,'transaction_read_only',current_setting('transaction_read_only'),'observed_at',clock_timestamp(),'acceptances',(SELECT coalesce(json_agg(to_jsonb(t)),'[]') FROM sys_modeldev_acceptances t WHERE tenant_id=2 AND resource_tenant_id='64c5c35a-f554-432c-b4bc-7b4f36f08420' AND execution_id IN ("+selection+")))")
print(json.dumps({'identity':identity,'modeldev':md,'governance':gov},separators=(',',':')))
'''
def digest(raw):return hashlib.sha256(raw).hexdigest()
def canonical(value):
 raw=bytes.fromhex(value.removeprefix('\\x'));obj=json.loads(raw)
 return {'sha256':digest(raw),'bytes':len(raw),'schema':obj.get('schema_version',obj.get('schema')),'kind':obj.get('kind'),'top_level_fields':sorted(obj),'training_image_present':bool(obj.get('training_image')),'runtime_plan_present':bool(obj.get('runtime_plan'))}
start=datetime.datetime.now(datetime.timezone.utc)
report={'schema':'ani.modeldev.original-owner-live-qualification.v1','started_at':start.isoformat(),'read_only':True,'fixed_execution_ids':IDS,'canonical_public_contents_retained':False,'cluster_mutations':0,'native_reader_experiment':'NOT_RUN'}
command=['ssh','-o','ConnectTimeout=10','-o','ServerAliveInterval=5','-o','ServerAliveCountMax=2',ENTRY,'python3 -']
try:
 process=subprocess.run(command,input=REMOTE.encode(),stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=150)
 report['sole_ssh_exit']=process.returncode
 if process.returncode:
  report['result']='BLOCKED_SOLE_ENTRY';report['safe_stderr_tail']=process.stderr.decode(errors='replace')[-1200:];report['identity_observation']='NOT_RUN_OR_NOT_RETURNED';report['canonical_read']='NOT_RUN_OR_NOT_RETURNED'
 else:
  data=json.loads(process.stdout);report['identity']=data['identity'];report['readback_private_envelope_sha256']=digest(process.stdout)
  report['database_observations']={key:{k:value[k] for k in ('database','database_user','transaction_read_only','observed_at')} for key,value in [('modeldev',data['modeldev']),('governance',data['governance'])]}
  report['modeldev_table_counts']={k:len(v) for k,v in data['modeldev'].items() if isinstance(v,list)}
  report['modeldev_executions']=[]
  for row in data['modeldev']['modeldev_executions']:
   report['modeldev_executions'].append({k:row[k] for k in ('tenant_id','execution_id','operation_id','spec_hash','intent_hash')}|{'intent':canonical(row['intent_canonical']),'snapshot':canonical(row['snapshot_canonical'])})
  report['governance_acceptances']=[]
  for row in data['governance']['acceptances']:
   report['governance_acceptances'].append({k:row[k] for k in ('id','tenant_id','resource_tenant_id','operation_id','execution_id','execution_spec_hash','intent_hash','dispatch_state','retry_blocked','last_error_code','stop_intent_generation','close_dispatch_state','close_retry_blocked','close_last_error_code')}|{'intent':canonical(row['intent_canonical']),'snapshot':canonical(row['snapshot_canonical'])})
  private='/home/chabking/tenantenv-20261101/owner-qualification-private-'+start.strftime('%Y%m%dT%H%M%S')+'.json'
  save="import os,sys; p="+repr(private)+"; fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600); f=os.fdopen(fd,'wb'); f.write(sys.stdin.buffer.read()); f.close()"
  transfer=subprocess.run(['ssh','-o','ConnectTimeout=10','-o','ServerAliveInterval=5','-o','ServerAliveCountMax=2','fedora','python3 -c '+__import__('shlex').quote(save)],input=process.stdout,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=40)
  report['fedora_private_input_transfer_exit']=transfer.returncode
  report['fedora_private_input']=private if transfer.returncode==0 else None
  report['result']='LIVE_READ_ONLY_FACTS_ACQUIRED_NATIVE_READER_PENDING' if transfer.returncode==0 else 'LIVE_FACTS_ACQUIRED_PRIVATE_FEDORA_TRANSFER_BLOCKED'
except subprocess.TimeoutExpired:
 report['result']='RESULT_UNKNOWN_OBSERVATION_TIMEOUT';report['sole_ssh_exit']='OBSERVATION_TIMEOUT_NOT_PROCESS_TERMINATION_PROOF'
except Exception as error:
 report['result']='FAILED_SAFE_EXTRACTION';report['error_kind']=type(error).__name__
report['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
(HERE/'original-owner-live-qualification.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report,indent=2))
