#!/usr/bin/env python3
"""Explicit commands for the retained .10-.12 lab; no automatic deployment."""
import argparse,base64,copy,hashlib,json,os,pathlib,re,shlex,subprocess,sys,time,uuid
import yaml

os.umask(0o077)
F=pathlib.Path('/home/chabking/workspace/cpu-p01-20260930-01')
D=F/'manual-ani-system-20261008';P=D/'.private';OWNER='ani-manual-20261008'
MAPPING={'ani-cpu-p01-system':'ani-system','ani-cpu-p01-tenant-a':'ani-kfp-manual-a','ani-cpu-p01-tenant-b':'ani-kfp-manual-b','ani-cpu-p01-control':'ani-modeldev-control','ani-cpu-p01-step':'ani-modeldev-step','cpu-p01-live-20261005':OWNER,'cpu-p01-step-exit-retention':'ani-modeldev-exit-retention','cpu-p01-step-exit-retention-tls':'ani-modeldev-exit-retention-tls','cpu-p01-step-exit-retention-8ea52cd8':'ani-modeldev-exit-retention-config'}
TRAIN_IMAGE='localhost/ani-cpu03@sha256:53f1200cc679ad827aaad9306310d5577e89c26e6876de6d61800655c07b5040'
SYSTEM='ani-system';TENANTS=['ani-kfp-manual-a','ani-kfp-manual-b']

def encoded(x):return json.dumps(x,ensure_ascii=False,separators=(',',':')).encode()
def sha(x):return hashlib.sha256(x).hexdigest()
def save(name,x,private=True):
 path=(P if private else D)/name;path.write_bytes(encoded(x)+b'\n');os.chmod(path,0o600)
def load(name):return json.loads((P/name).read_bytes())
def kube(args,body=None):
 r=subprocess.run([sys.executable,str(F/'cluster-ssh.py'),'172.16.101.10',shlex.join(['sudo','-n','kubectl','--request-timeout=30s',*args])],input=body,capture_output=True,timeout=45)
 if r.returncode:raise RuntimeError('kubectl failed; details suppressed for private material: '+' '.join(args[:4]))
 return r.stdout
def get(kind,name=None,ns=None):
 args=['get',kind]+([name] if name else [])+(['-n',ns] if ns else [])+['-o','json'];return json.loads(kube(args))
def guard():assert get('namespace','kube-system')['metadata']['uid']=='5277649d-e28d-4a0a-ae76-8354365c63bc','Wrong cluster'
def rewrite(x,mapping=MAPPING):
 if isinstance(x,dict):return {k:rewrite(v,mapping) for k,v in x.items()}
 if isinstance(x,list):return [rewrite(v,mapping) for v in x]
 if isinstance(x,str):
  for old,new in sorted(mapping.items(),key=lambda v:-len(v[0])):x=re.sub(r'(?<![A-Za-z0-9-])'+re.escape(old)+r'(?![A-Za-z0-9-])',lambda _:new,x)
 return x
def clean(x):
 x=copy.deepcopy(x);x.pop('status',None);m=x['metadata']
 for k in ['uid','resourceVersion','generation','creationTimestamp','managedFields','ownerReferences','deletionTimestamp','deletionGracePeriodSeconds','finalizers']:m.pop(k,None)
 for k in list(m.get('annotations',{})):
  if k.startswith(('kubectl.kubernetes.io/','deployment.kubernetes.io/','ani.io/modeldev-resource-intent')):m['annotations'].pop(k)
 m.setdefault('labels',{})['ani.io/managed-by']=OWNER
 if x['kind']=='Service':
  for k in ['clusterIP','clusterIPs','ipFamilies','ipFamilyPolicy','healthCheckNodePort']:x['spec'].pop(k,None)
  for port in x['spec'].get('ports',[]):port.pop('nodePort',None)
 if x['kind']=='PersistentVolumeClaim':
  x['spec'].pop('volumeName',None);m['annotations']={}
 return rewrite(x)
def find(seed,kind,name,ns='ani-cpu-p01-system'):
 return next(x for x in seed['items']+seed['global'] if x['kind']==kind and x['metadata']['name']==name and x['metadata'].get('namespace')==ns)
def listfile(name,rows):save(name,{'apiVersion':'v1','kind':'List','items':rows})
def podspec(image,code,volumes,mounts,uid=10001):
 return {'restartPolicy':'Never','automountServiceAccountToken':False,'securityContext':{'runAsNonRoot':True,'runAsUser':uid,'runAsGroup':uid,'fsGroup':uid,'seccompProfile':{'type':'RuntimeDefault'}},'containers':[{'name':'check','image':image,'imagePullPolicy':'Never','command':['/opt/venv/bin/python','-c',code],'resources':{'requests':{'cpu':'50m','memory':'64Mi'},'limits':{'cpu':'250m','memory':'256Mi'}},'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},'volumeMounts':mounts}],'volumes':volumes}
def job(name,ns,spec):return {'apiVersion':'batch/v1','kind':'Job','metadata':{'name':name,'namespace':ns,'labels':{'ani.io/managed-by':OWNER}},'spec':{'backoffLimit':0,'activeDeadlineSeconds':180,'template':{'metadata':{'labels':{'ani.io/managed-by':OWNER}},'spec':spec}}}
def prepare():
 seed=load('original-resources.json');rows=[]
 for old in ['ani-cpu-p01-system','ani-cpu-p01-tenant-a','ani-cpu-p01-tenant-b']:
  x=next(x for x in seed['namespaces'] if x['metadata']['name']==old);x=clean(x);rows.append(x)
 for x in seed['items']:
  ns=x['metadata'].get('namespace');kind=x['kind'];name=x['metadata']['name']
  if ns not in ['ani-cpu-p01-system','ani-cpu-p01-tenant-a','ani-cpu-p01-tenant-b']:continue
  if kind in ['NetworkPolicy','Role','RoleBinding'] and ('fault-' in name or 'publication-provider-' in name):continue
  if kind in ['ServiceAccount','Role','RoleBinding','NetworkPolicy','ResourceQuota','LimitRange']:rows.append(clean(x))
  elif kind=='ConfigMap' and ns!='ani-cpu-p01-system' and name!='modeldev-step-owner' and not name.startswith(('cpu-p01-','modeldev-step-fault-','kube-root-ca')):rows.append(clean(x))
  elif kind=='Secret' and (ns=='ani-cpu-p01-system' or name in ['ani-kfp-ca','mlpipeline-minio-artifact']):
   v=clean(x)
   # Preserve certificates/keys and object-store identities. Only textual config is rebound.
   if name=='governance-config':v['data']['bootstrap.json']=base64.b64encode(encoded(rewrite(json.loads(base64.b64decode(x['data']['bootstrap.json']))))).decode()
   rows.append(v)
 for x in seed['global']:
  if x['kind'] in ['ClusterRole','ClusterRoleBinding'] and x['metadata']['name'] in ['ani-cpu-p01-control','ani-cpu-p01-step']:rows.append(clean(x))
 for x in load('shared-owned.json'):
  v=clean(x);v['metadata']['name']='ani-manual-'+x['metadata']['name'];rows.append(v)
 pvc=clean(find(seed,'PersistentVolumeClaim','modeldev-catalogue-block'));rows.append(pvc)
 # A new certificate matches the new webhook Service DNS; old leaf certificates cannot be relabeled.
 crt=P/'new-webhook.crt';key=P/'new-webhook.key'
 if not crt.exists():
  r=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(crt),'-days','365','-subj','/CN=ani-modeldev-exit-retention.ani-system.svc','-addext','subjectAltName=DNS:ani-modeldev-exit-retention.ani-system.svc,DNS:ani-modeldev-exit-retention.ani-system.svc.cluster.local'],capture_output=True)
  assert r.returncode==0,'Webhook certificate generation failed'
 tls=next(x for x in rows if x['kind']=='Secret' and x['metadata']['name']=='ani-modeldev-exit-retention-tls');tls['data']={'tls.crt':base64.b64encode(crt.read_bytes()).decode(),'tls.key':base64.b64encode(key.read_bytes()).decode()}
 # The new catalogue is populated from canonical immutable Release files, not from an old PVC UID.
 rows.append({'apiVersion':'v1','kind':'Secret','metadata':{'name':'modeldev-catalogue-seed','namespace':SYSTEM},'type':'Opaque','data':{'files.json':base64.b64encode(encoded(load('catalogue-files.json'))).decode()}})
 code="""import base64,json,os,pathlib,hashlib
src=json.load(open('/seed/files.json'));root=pathlib.Path('/catalogue')
for name,value in src.items():
 p=root/name;assert not pathlib.PurePosixPath(name).is_absolute() and '..' not in pathlib.PurePosixPath(name).parts
 raw=base64.b64decode(value);p.parent.mkdir(parents=True,exist_ok=True)
 if p.exists():assert p.read_bytes()==raw
 else:
  with p.open('xb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
print(json.dumps({'files':len(src),'uid':os.getuid(),'result':'PASS'}))
"""
 restore=job('modeldev-catalogue-restore',SYSTEM,podspec(TRAIN_IMAGE,code,[{'name':'seed','secret':{'secretName':'modeldev-catalogue-seed','defaultMode':288}},{'name':'catalogue','persistentVolumeClaim':{'claimName':'modeldev-catalogue-block'}}],[{'name':'seed','mountPath':'/seed','readOnly':True},{'name':'catalogue','mountPath':'/catalogue'}]))
 listfile('foundation.json',rows);listfile('catalogue-restore.json',[restore]);probes=[]
 for ns in TENANTS:
  claim={'apiVersion':'v1','kind':'PersistentVolumeClaim','metadata':{'name':'ani-kfp-workspace-manual-probe','namespace':ns,'labels':{'ani.io/managed-by':OWNER}},'spec':{'storageClassName':'ani-cephfs','accessModes':['ReadWriteMany'],'resources':{'requests':{'storage':'1Gi'}}}}
  code="""import json,os,pathlib
assert os.getuid()==10001 and os.getgid()==10001
p=pathlib.Path('/proof/manual-probe.tmp');p.write_bytes(b'cpu-p01-manual-10001');q=p.with_suffix('.done');os.replace(p,q);assert q.read_bytes()==b'cpu-p01-manual-10001'
print(json.dumps({'uid':os.getuid(),'gid':os.getgid(),'atomic_write_read':True,'result':'PASS'}))
"""
  probes+=[claim,job('manual-workspace-probe',ns,podspec(TRAIN_IMAGE,code,[{'name':'proof','persistentVolumeClaim':{'claimName':claim['metadata']['name']}}],[{'name':'proof','mountPath':'/proof'}]))]
 listfile('workspace-probes.json',probes)
 print(json.dumps({'phase':'PREPARED_NOT_DEPLOYED','private_manifests':['foundation.json','catalogue-restore.json','workspace-probes.json']}))
def pipeline():
 from kfp import Client
 guard();ns=get('namespace',TENANTS[0]);ledger=P/'pipeline-receipt.json'
 assert not ledger.exists(),'Inspect the retained pipeline phase before retry; do not blindly upload again'
 secret=get('secret','ani-kfp-entry-tls','kubeflow');ca=P/'kfp-entry-ca.crt';ca.write_bytes(base64.b64decode(secret['data']['ca.crt']))
 token=kube(['create','token','modeldev-control','-n',SYSTEM,'--audience=pipelines.kubeflow.org','--duration=1h']).decode().strip()
 client=Client(host='https://172.16.101.10:30445',namespace=TENANTS[0],existing_token=token,ssl_ca_cert=str(ca),verify_ssl=True)
 ir=D/'general-cpu.yaml';r={'namespace':TENANTS[0],'namespace_uid':ns['metadata']['uid'],'pipeline_ir_sha256':sha(ir.read_bytes()),'phase':'UPLOAD_PIPELINE_PENDING'};save(ledger.name,r)
 created=client.upload_pipeline(str(ir),pipeline_name='cpu-p01-manual-'+ns['metadata']['uid'][:8],namespace=TENANTS[0]);r['pipeline_id']=created.pipeline_id;r['phase']='UPLOAD_VERSION_PENDING';save(ledger.name,r)
 version=client.upload_pipeline_version(str(ir),pipeline_version_name='cpu-p01-'+r['pipeline_ir_sha256'][:12],pipeline_id=created.pipeline_id);r['pipeline_version_id']=version.pipeline_version_id;r['phase']='CREATE_EXPERIMENT_PENDING';save(ledger.name,r)
 experiment=client.create_experiment(name='cpu-p01-manual-'+ns['metadata']['uid'][:8],namespace=TENANTS[0]);r['experiment_id']=experiment.experiment_id;r['phase']='VERIFY_READBACK_PENDING';save(ledger.name,r)
 assert client.get_pipeline_version(r['pipeline_id'],r['pipeline_version_id']).pipeline_version_id==r['pipeline_version_id']
 r['phase']='CONFIRMED';save(ledger.name,r)
 print(json.dumps(r))
def render():
 guard();seed=load('original-resources.json');actual={};proofs=[]
 for ns in [SYSTEM,*TENANTS]:
  x=get('namespace',ns);assert x['metadata']['labels']['ani.io/managed-by']==OWNER
  actual[ns]=x['metadata']['uid']
 for ns in TENANTS:
  j=get('job','manual-workspace-probe',ns);assert any(c['type']=='Complete' and c['status']=='True' for c in j['status'].get('conditions',[]))
  proofs.append({'namespace':ns,'job_uid':j['metadata']['uid'],'status':j['status']})
 restore=get('job','modeldev-catalogue-restore',SYSTEM);assert any(c['type']=='Complete' and c['status']=='True' for c in restore['status'].get('conditions',[]))
 rbac=[]
 for verb,resource,ns in [('get','pods',TENANTS[0]),('create','trainjobs.trainer.kubeflow.org',TENANTS[0]),('get','workflows.argoproj.io',TENANTS[0])]:
  out=kube(['auth','can-i',verb,resource,'-n',ns,'--as=system:serviceaccount:'+SYSTEM+':modeldev-control']).decode().strip();assert out=='yes';rbac.append({'verb':verb,'resource':resource,'namespace':ns,'allowed':True})
 runtimes=[]
 for x in seed['global']:
  if x['kind']!='ClusterTrainingRuntime':continue
  now=get('clustertrainingruntime',x['metadata']['name']);assert now['spec']==x['spec'],'Retained Runtime has drifted'
  runtimes.append({'name':x['metadata']['name'],'uid':now['metadata']['uid'],'spec_sha256':sha(json.dumps(now['spec'],sort_keys=True,separators=(',',':')).encode())})
 receipt=load('pipeline-receipt.json');assert receipt['phase']=='CONFIRMED' and receipt['namespace_uid']==actual[TENANTS[0]]
 proof={'cluster_uid':get('namespace','kube-system')['metadata']['uid'],'namespace_uids':actual,'workspace_write_probes':proofs,'catalogue_restore_job_uid':restore['metadata']['uid'],'rbac_checks':rbac,'runtimes':runtimes,'pipeline':receipt}
 save('environment-receipt.json',proof,False);proofsha=sha(encoded(proof));uidmap={x['metadata']['uid']:actual[MAPPING[x['metadata']['name']]] for x in seed['namespaces'] if x['metadata']['name'] in ['ani-cpu-p01-system','ani-cpu-p01-tenant-a','ani-cpu-p01-tenant-b']}
 rows=[]
 md=find(seed,'Deployment','ani-modeldev');currentcm=next(v['configMap']['name'] for v in md['spec']['template']['spec']['volumes'] if v['name']=='config');cm=clean(find(seed,'ConfigMap',currentcm));cm=rewrite(cm,uidmap);config=yaml.safe_load(cm['data']['config.yaml'])
 step=rewrite(clean(find(seed,'ConfigMap','modeldev-step-owner','ani-cpu-p01-tenant-a')),uidmap)
 newstep=json.loads(step['data']['config.json']);newstep['namespace_uid']=actual[TENANTS[0]];step['data']['config.json']=encoded(newstep).decode();rows.append(step)
 bind=json.loads(cm['data']['dispatch-binding.json']);bind['environment']['binding_id']=str(uuid.uuid4());bind['environment']['binding_digest']=proofsha;bind['environment']['experiment_id']=receipt['experiment_id'];bind['owner']['revision_sha256']=sha(step['data']['config.json'].encode())
 cm['data']['dispatch-binding.json']=encoded(bind).decode();config['runtime']['binding_sha256']=sha(cm['data']['dispatch-binding.json'].encode())
 files=load('catalogue-files.json');fresh={};catalogue={};factpaths=[]
 selected={'success':'r6-success.facts.json','fail':'r6-fail.facts.json','stop':'r7-slow-stop.facts.json','deadline':'r6-deadline.facts.json'}
 original=copy.deepcopy(cm['data'])
 for k in list(cm['data']):
  if k.endswith('.facts.json'):del cm['data'][k]
 for mode,k in selected.items():
  facts=json.loads(original[k]);release=json.loads(base64.b64decode(files[facts['release_id']+'.json']));assert release['pipeline_ir_sha256']==receipt['pipeline_ir_sha256'],'Wrong retained IR'
  release['release_id']=str(uuid.uuid4());release['pipeline_id']=receipt['pipeline_id'];release['pipeline_version_id']=receipt['pipeline_version_id'];raw=encoded(release);digest=sha(raw)
  facts['release_id']=release['release_id'];facts['release_digest']=digest;catalogue[release['release_id']+'.json']=base64.b64encode(raw).decode();fresh[mode]={'preset_id':release['preset_id'],'release_id':release['release_id'],'release_digest':digest,'canonical_release':base64.b64encode(raw).decode()}
  env=facts['environment'];env.update(bind['environment'])
  facts['environment_evidence']={'reference':str(D/'environment-receipt.json'),'sha256':proofsha};facts['application_evidence']={'reference':str(D/'environment-receipt.json'),'sha256':proofsha}
  name='manual-'+mode+'.facts.json';cm['data'][name]=encoded(facts).decode();factpaths.append({'path':'/etc/modeldev/facts/'+name,'sha256':sha(cm['data'][name].encode())})
 config['command']['admission_resolution']['facts_files']=factpaths;save('release-imports.json',fresh)
 seedsecret={'apiVersion':'v1','kind':'Secret','metadata':{'name':'modeldev-manual-releases','namespace':SYSTEM},'type':'Opaque','data':{'files.json':base64.b64encode(encoded(catalogue)).decode()}}
 restoreobj=load('catalogue-restore.json')['items'][0];restoreobj['metadata']['name']='modeldev-manual-releases';restoreobj['spec']['template']['spec']['volumes'][0]['secret']['secretName']='modeldev-manual-releases';listfile('release-seed.json',[seedsecret,restoreobj])
 for f in config['command']['admission_resolution']['facts_files']:
  name=pathlib.Path(f['path']).name;f['sha256']=sha(cm['data'][name].encode())
 cm['data']['config.yaml']=yaml.safe_dump(config,sort_keys=False);rows.append(cm)
 for x in seed['items']:
  if x['metadata'].get('namespace')!='ani-cpu-p01-system':continue
  if x['kind']=='Service' and x['metadata']['name'] in ['ani-modeldev','ani-governance','cpu-p01-step-exit-retention']:rows.append(clean(x))
  if x['kind']=='Deployment' and x['metadata']['name'] in ['ani-modeldev','ani-governance','cpu-p01-step-exit-retention']:
   dep=clean(x)
   if x['metadata']['name']=='ani-modeldev':
    container=dep['spec']['template']['spec']['containers'][0]
    container['volumeMounts']=[v for v in container['volumeMounts'] if not v['mountPath'].startswith('/etc/modeldev/facts/')]
    container['volumeMounts'] += [{'name':'config','mountPath':f['path'],'subPath':pathlib.Path(f['path']).name,'readOnly':True} for f in factpaths]
   rows.append(dep)
 webhookdep=find(seed,'Deployment','cpu-p01-step-exit-retention');webhookcm=next(v['configMap']['name'] for v in webhookdep['spec']['template']['spec']['volumes'] if 'configMap' in v)
 rows.append(rewrite(clean(find(seed,'ConfigMap',webhookcm)),uidmap))
 webhooks=[]
 for x in seed['global']:
  if x['kind']=='MutatingWebhookConfiguration' and x['metadata']['name']=='cpu-p01-step-exit-retention':
   x=clean(x)
   for w in x['webhooks']:w['clientConfig']['caBundle']=base64.b64encode((P/'new-webhook.crt').read_bytes()).decode()
   webhooks.append(x)
 # ConfigMaps containing namespace UIDs are recreated once while no workloads exist.
 listfile('application.json',rows);listfile('webhook.json',webhooks);save('new-step-owner.json',step)
 print(json.dumps({'phase':'RENDERED_NOT_DEPLOYED','namespace_uids':actual,'environment_receipt_sha256':proofsha,'application_manifest':str(P/'application.json')}))
def forward():
 guard();procs=[]
 try:
  cmd=shlex.join(['sudo','-n','timeout','620s','kubectl','-n',SYSTEM,'port-forward','--address','127.0.0.1','service/ani-governance','7788:7788'])
  procs.append(subprocess.Popen([sys.executable,str(F/'cluster-ssh.py'),'172.16.101.10',cmd]))
  time.sleep(2)
  procs.append(subprocess.Popen([sys.executable,str(F/'cluster-ssh.py'),'172.16.101.10','--forward','127.0.0.1:19778:127.0.0.1:7788']))
  ip=get('service','ani-rustfs-svc','ani-platform')['spec']['clusterIP']
  procs.append(subprocess.Popen([sys.executable,str(F/'cluster-ssh.py'),'172.16.101.10','--forward','127.0.0.1:19900:'+ip+':9000']))
  print('Fedora BFF http://127.0.0.1:19778; S3 TLS tunnel 127.0.0.1:19900; expires in 10 minutes.',flush=True)
  while all(p.poll() is None for p in procs):time.sleep(1)
 finally:
  for p in procs:
   if p.poll() is None:p.terminate()
  for p in procs:
   try:p.wait(timeout=5)
   except subprocess.TimeoutExpired:p.kill();p.wait()
def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('command',choices=['prepare','pipeline','render','forward','kubectl']);parser.add_argument('args',nargs=argparse.REMAINDER);a=parser.parse_args()
 if a.command in ('prepare','render'):
  parser.error('Legacy date-bound deployment rendering is retired. Use deploy/modeldev-webhook and docs/modeldev-webhook.md for the unified component; retained code and manifests are historical evidence. No cluster registration is performed.')
 if a.command=='kubectl':
  guard();sys.stdout.buffer.write(kube(a.args));return
 if a.command=='prepare':prepare()
 elif a.command=='pipeline':pipeline()
 elif a.command=='render':render()
 else:forward()
if __name__=='__main__':
 try:main()
 except Exception as error:
  import traceback
  if P.is_dir():(P/'manual-last-error.txt').write_text(traceback.format_exc())
  print('STOPPED: '+type(error).__name__+'; private diagnostic: '+str(P/'manual-last-error.txt')+'; inspect the recorded phase before retry.',file=sys.stderr);sys.exit(1)
