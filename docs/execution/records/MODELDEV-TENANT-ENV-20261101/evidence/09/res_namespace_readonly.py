import base64,datetime,hashlib,json,re,subprocess,uuid

def run(args):
 r=subprocess.run(args,capture_output=True,timeout=45)
 if r.returncode:raise RuntimeError('read operation failed: '+args[0])
 return r.stdout

def kget(*args):return json.loads(run(['kubectl',*args,'-o','json']))
def sql(db,q):
 args=['kubectl','-n','ani-platform','exec','-i','postgresql-0','--','sh','-c','PGPASSWORD="${POSTGRES_PASSWORD:-${POSTGRES_POSTGRES_PASSWORD:-}}" PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=15000" exec psql -U postgres -d '+db+' -At -v ON_ERROR_STOP=1']
 r=subprocess.run(args,input=q.encode(),capture_output=True,timeout=40)
 if r.returncode:raise RuntimeError('read-only SQL failed for '+db)
 return json.loads(r.stdout)
context=run(['kubectl','config','current-context']).decode().strip()
api=json.loads(run(['kubectl','config','view','--minify','-o','json']))['clusters'][0]['cluster']['server']
ks=kget('get','ns','kube-system')['metadata']['uid']
raw=base64.b64decode(kget('-n','ani-system','get','secret','ani-resource-private')['data']['config.yaml']).decode()
m=re.search(r'^\s*cluster_id:\s*["\']?([a-f0-9-]{36})',raw,re.M);cluster=m.group(1) if m else None
del raw
expected=('kubernetes-admin@ani-kcn-full','https://lb.kubesphere.local:6443','3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','f39bf6e2-f3ed-4191-ada3-caeebf86c220')
if (context,api,ks,cluster)!=expected:raise RuntimeError('full target identity mismatch')
if run(['whoami']).decode().strip()!='kubercloud':raise RuntimeError('SSH user mismatch')
out={'schema':'ani.resource.namespace-uid.pre-migration-readonly.v1','read_only':True,'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'ssh':'kubercloud@100.64.128.12','target':dict(zip(['context','apiserver','kube_system_uid','cluster_id'],expected))}
dbs=sql('postgres',"SELECT coalesce(json_agg(datname),'[]') FROM pg_database WHERE datallowconn AND NOT datistemplate AND datname LIKE '%resource%'")
found=[]
for db in dbs:
 if sql(db,"SELECT to_json(to_regclass('public.network_tenant_namespaces') IS NOT NULL)"):found.append(db)
if len(found)!=1:raise RuntimeError('expected exactly one resource Network database')
db=found[0];out['database']=db
cols=sql(db,"SELECT coalesce(json_agg(column_name),'[]') FROM information_schema.columns WHERE table_schema='public' AND table_name='network_tenant_namespaces'")
out['namespace_uid_column_present']='namespace_uid' in cols
rows=sql(db,"SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT tenant_id,cluster_id,namespace,"+('namespace_uid' if out['namespace_uid_column_present'] else 'NULL::text AS namespace_uid')+" FROM network_tenant_namespaces ORDER BY tenant_id,cluster_id)x")
out['identity_rows_total']=len(rows);out['identity_rows_served_cluster']=sum(r['cluster_id']==cluster for r in rows);out['rows']=[];out['blockers']=[]
for r in rows:
 if r['cluster_id']!=cluster:continue
 item=dict(r);item['expected_namespace']='tenant-'+r['tenant_id'];reasons=[]
 try:
  if str(uuid.UUID(r['tenant_id']))!=r['tenant_id'] or uuid.UUID(r['tenant_id']).int==0:reasons.append('NON_CANONICAL_TENANT_UUID')
 except ValueError:reasons.append('INVALID_TENANT_UUID')
 if r['namespace']!=item['expected_namespace']:reasons.append('REGISTERED_NAME_DIFFERS_FROM_STANDARD')
 # Match the production tool's exact standard-name GET, never a namespace list/adoption.
 read=subprocess.run(['kubectl','get','namespace',item['expected_namespace'],'-o','json'],capture_output=True,timeout=25)
 if read.returncode:
  reasons.append('STANDARD_NAMESPACE_GET_FAILED');item['get_error']='NotFound' if b'NotFound' in read.stderr else 'GET_FAILED'
 else:
  ns=json.loads(read.stdout);meta=ns['metadata'];labels=meta.get('labels',{});item['live']={'name':meta['name'],'uid':meta['uid'],'phase':ns.get('status',{}).get('phase'),'deleting':bool(meta.get('deletionTimestamp')),'tenant_label':labels.get('network.ani.io/tenant-id'),'managed_by_label':labels.get('network.ani.io/managed-by'),'resource_version':meta.get('resourceVersion')}
  if labels.get('network.ani.io/tenant-id')!=r['tenant_id'] or labels.get('network.ani.io/managed-by')!='ani-network-service':reasons.append('OWNERSHIP_MISMATCH')
  if ns.get('status',{}).get('phase')!='Active' or meta.get('deletionTimestamp'):reasons.append('NOT_ACTIVE')
  if r['namespace_uid'] is not None and r['namespace_uid']!=meta['uid']:reasons.append('ORIGINAL_UID_DIFFERS')
 item['reasons']=reasons;out['rows'].append(item)
 if reasons:out['blockers'].append({'tenant_id':r['tenant_id'],'namespace':r['namespace'],'reasons':reasons})
out['null_uid_before_count']=sum(r['namespace_uid'] is None for r in out['rows'])
# Read-only counts are preservation inventory, not a VM/Instance execution test.
tables=sql(db,"SELECT coalesce(json_agg(tablename),'[]') FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'network_%' OR tablename LIKE 'vm_%' OR tablename LIKE 'instance_%')")
out['preserved_table_counts']={t:sql(db,'SELECT count(*) FROM public."'+t+'"') for t in sorted(tables) if t in ['network_tenant_namespaces','network_vpcs','network_subnets','network_eips','network_provider_bindings','vm_instances','vm_operations','instance_instances','instance_operations']}
asset_tables=sql(db,"SELECT coalesce(json_agg(table_schema||'.'||table_name),'[]') FROM information_schema.tables WHERE (table_schema='vm' AND table_name IN ('virtual_machines','disks','operations')) OR (table_schema='instance' AND table_name IN ('instances','instance_operations','workspace_bindings'))")
out['preserved_vm_instance_table_counts']={t:sql(db,'SELECT count(*) FROM '+t) for t in sorted(asset_tables)}
out['migration_applied']=False;out['backfill_executed']=False;out['service_updated']=False;out['vm_instance_mutations']=False
out['plan_status']='BLOCKED' if out['blockers'] else 'PRE_MIGRATION_IDENTITIES_MATCH'
out['apply_plan_sha256']=None
out['apply_plan_note']='No production-tool plan/apply executed: UID column not migrated or admission writers not paused; this is a read-only pre-migration equivalent check only.'
print(json.dumps(out,ensure_ascii=False,indent=2))
