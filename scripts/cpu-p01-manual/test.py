#!/usr/bin/env python3
"""Human-invoked real BFF tests for the retained CPU-P01 lab."""
import argparse,base64,copy,json,pathlib,re,sys,time,urllib.error,urllib.request,uuid
import manual as m
from acceptance import API,stop_training

BASE='http://127.0.0.1:19778';ROOT='/admin/v1/modeldev'
def api(who='a'):return API(BASE,str(m.P/(who+'.token')))
def call(method,path,body=None,expected=(200,),who='a'):return api(who).call(method,ROOT+path,body,expected)
def login():
 settings=json.loads((m.F/'.private/live-governance-20261005/bootstrap-settings.json').read_text())
 identities=[('a',settings['tenants'][0]),('b',settings['tenants'][1]),('ungranted',{'code':settings['tenants'][0]['code'],'username':settings['run']+'-no-model','password_file':str(m.F/'.private/live-governance-20261005/ungranted.password')})]
 for who,row in identities:
  body={'tenant_name':row['code'],'username':row['username'],'password':pathlib.Path(row['password_file']).read_text().strip()}
  request=urllib.request.Request(BASE+'/api/v1/auth/password/login',m.encoded(body),{'Content-Type':'application/json'},method='POST')
  with urllib.request.urlopen(request,timeout=30) as response:reply=json.load(response)
  token=reply['access_token'];(m.P/(who+'.token')).write_text(token);print(json.dumps({'login':who,'token_saved_private':True}))
def create(obj):
 m.guard();ns=obj['metadata']['namespace'];assert ns in [m.SYSTEM,*m.TENANTS] and m.get('namespace',ns)['metadata']['labels'].get('ani.io/managed-by')==m.OWNER
 obj['metadata'].setdefault('labels',{})['ani.io/managed-by']=m.OWNER
 old=m.kube(['get',obj['kind'],obj['metadata']['name'],'-n',ns,'--ignore-not-found','-o','json'])
 assert not old.strip(),'Existing Job/input requires inspection before retry'
 return json.loads(m.kube(['create','-f','-','-o','json'],m.encoded(obj)))
def waitjob(name,ns,container):
 deadline=time.monotonic()+190
 while time.monotonic()<deadline:
  job=m.get('job',name,ns);conditions=job.get('status',{}).get('conditions',[])
  if any(c['status']=='True' and c['type'] in ['Complete','Failed'] for c in conditions):break
  time.sleep(2)
 else:raise RuntimeError('Job timed out; inspect retained Job before repeating')
 pods=m.get('pods',ns=ns)['items'];pod=next(x for x in pods if x['metadata'].get('labels',{}).get('job-name')==name)
 states=[s['state'].get('terminated',{}) for s in pod['status'].get('containerStatuses',[]) if s['name']==container]
 assert len(states)==1 and states[0].get('exitCode')==0,'Job failed; inspect its private log and Pod status'
 return m.kube(['logs','job/'+name,'-n',ns,'-c',container])
def manage(command,name,request):
 m.guard();ledger=m.P/(name+'.operation.json');assert not ledger.exists(),'Inspect retained operation; no blind management retry'
 m.save(ledger.name,{'phase':'PENDING','command':command,'request_sha256':m.sha(m.encoded(request))})
 create({'apiVersion':'v1','kind':'Secret','metadata':{'name':name,'namespace':m.SYSTEM},'type':'Opaque','data':{'token':base64.b64encode((m.P/'a.token').read_bytes()).decode(),'request.json':base64.b64encode(m.encoded(request)).decode()}})
 security={'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}}
 code="""import os,pathlib,shutil
os.umask(0o077);p=pathlib.Path('/private');(p/'config').mkdir(mode=0o700)
for src,dst in [('/input/token',p/'token'),('/input/request.json',p/'request.json'),('/config/bootstrap.json',p/'config/bootstrap.json')]:
 with open(src,'rb') as r,open(dst,'xb') as w:shutil.copyfileobj(r,w)
 os.chmod(dst,0o600)
"""
 dep=m.get('deployment','ani-governance',m.SYSTEM);g=dep['spec']['template']['spec']['containers'][0]
 env=[{'name':k,'value':v} for k,v in {'ANI_MODELDEV_ADDR':'ani-modeldev.ani-system.svc:9000','ANI_MODELDEV_CA':'/mtls/ca.crt','ANI_MODELDEV_CERT':'/mtls/tls.crt','ANI_MODELDEV_KEY':'/mtls/tls.key','ANI_MODELDEV_TIMEOUT':'30s'}.items()]
 spec={'serviceAccountName':'governance-runtime','automountServiceAccountToken':False,'restartPolicy':'Never','securityContext':{'runAsNonRoot':True,'runAsUser':65532,'runAsGroup':65532,'fsGroup':65532,'seccompProfile':{'type':'RuntimeDefault'}},'volumes':[{'name':'input','secret':{'secretName':name,'defaultMode':288}},{'name':'config','secret':{'secretName':'governance-config','defaultMode':288}},{'name':'mtls','secret':{'secretName':'modeldev-mtls','defaultMode':288}},{'name':'private','emptyDir':{'medium':'Memory','sizeLimit':'8Mi'}},{'name':'tmp','emptyDir':{'sizeLimit':'64Mi'}}],'initContainers':[{'name':'copy','image':m.TRAIN_IMAGE,'imagePullPolicy':'Never','command':['/opt/venv/bin/python','-c',code],'securityContext':security,'resources':{'requests':{'cpu':'10m','memory':'32Mi'},'limits':{'cpu':'250m','memory':'128Mi'}},'volumeMounts':[{'name':'input','mountPath':'/input','readOnly':True},{'name':'config','mountPath':'/config','readOnly':True},{'name':'private','mountPath':'/private'}]}],'containers':[{'name':'admin','image':g['image'],'imagePullPolicy':'Never','command':['/app/bin/admin'],'args':[command,'--conf','/private/config','--token-file','/private/token','--request-file','/private/request.json'],'env':env,'securityContext':security,'resources':{'requests':{'cpu':'100m','memory':'64Mi'},'limits':{'cpu':'500m','memory':'256Mi'}},'volumeMounts':[{'name':'private','mountPath':'/private','readOnly':True},{'name':'mtls','mountPath':'/mtls','readOnly':True},{'name':'tmp','mountPath':'/tmp'}]}]}
 obj=m.job(name,m.SYSTEM,spec);obj['spec']['template']['metadata']['labels']['app']='ani-governance';create(obj)
 raw=waitjob(name,m.SYSTEM,'admin');(m.P/(name+'.log')).write_bytes(raw)
 reply=json.loads(raw.strip().splitlines()[-1]);m.save(ledger.name,{'phase':'CONFIRMED','command':command,'response':reply});print(json.dumps(reply));return reply
def enable(mode):
 request=m.load('release-imports.json')[mode]
 manage('modeldev-import-release','manual-import-'+mode,request)
 views=call('GET','/presets?page_size=100')['presets'];view=next((x for x in views if x['preset_id']==request['preset_id']),{})
 change={k:request[k] for k in ['preset_id','release_id','release_digest']};change.update(expected_generation=int(view.get('binding_generation',0)),reason='manual namespace redeployment',evidence_reference=str(m.D/'environment-receipt.json'))
 manage('modeldev-enable','manual-enable-'+mode,change)
def run(name,mode):
 assert re.fullmatch('[a-z][a-z0-9-]{0,30}',name)
 requestfile=name+'.request.json'
 if not (m.P/requestfile).exists():
  inputs=call('GET','/input-versions?state=READY&page_size=100')['input_versions'];inp=next(x for x in inputs if int(x['row_count'])==1024 and int(x['feature_count'])==16)
  preset=m.load('release-imports.json')[mode]['preset_id'];m.save(requestfile,{'name':'manual-'+name,'kind':'GENERAL_TRAINING','preset_id':preset,'dataset_version_id':inp['input_version_id'],'idempotency_key':str(uuid.uuid4()),'general_parameters':[{'name':'epochs','type':'INTEGER','value':'3'},{'name':'batch_size','type':'INTEGER','value':'64'},{'name':'learning_rate','type':'DECIMAL','value':'0.01'}]})
 body=m.load(requestfile);reply=call('POST','/executions',body,(202,));m.save(name+'.receipt.json',reply)
 again=call('POST','/executions',body,(202,));assert again['execution_id']==reply['execution_id'] and again['operation_id']==reply['operation_id'] and again['replayed'];print(json.dumps({'receipt':reply,'idempotency':'PASS'}))
 if mode=='stop':stop_training(api(),{'request':body,'execution_id':reply['execution_id'],'operation_id':reply['operation_id']})
def execution(name):return m.load(name+'.receipt.json')['execution_id']
def wait(name,expected):
 target=execution(name);deadline=time.monotonic()+900
 while time.monotonic()<deadline:
  result=call('GET','/executions/'+target,expected=(200,404))
  if result.get('http_status')==404:time.sleep(3);continue
  view=result['execution'];print(json.dumps({k:view.get(k) for k in ['execution_id','compute_state','delivery_state','close_state']}),flush=True)
  assert view.get('close_state')!='NEEDS_REVIEW','Execution requires explicit review'
  if view.get('close_state')=='CLOSED':
   if expected!='CLOSED':assert view.get('compute_state')==expected
   if expected=='SUCCEEDED':assert view.get('delivery_state')=='PUBLISHED'
   m.save(name+'.view.json',view);print(json.dumps({'result':'PASS','expected_compute':expected}));return
  time.sleep(3)
 raise RuntimeError('Execution did not close within 900 seconds')
def verify(name):
 target=execution(name);ns=m.TENANTS[0];jobname='manual-verify-'+name
 # Each verifier has no workspace PVC, storage key, or automatic SA token.
 config={'mode':'verify','base_url':'http://ani-governance.ani-system.svc:7788','token_file':'/input/token','execution_id':target,'s3_ca_file':'/input/s3-ca.crt'}
 step=m.get('configmap','modeldev-step-owner',ns)
 create({'apiVersion':'v1','kind':'Secret','metadata':{'name':jobname,'namespace':ns},'type':'Opaque','data':{'token':base64.b64encode((m.P/'a.token').read_bytes()).decode(),'config.json':base64.b64encode(m.encoded(config)).decode(),'s3-ca.crt':base64.b64encode(step['data']['s3-ca.crt'].encode()).decode()}})
 create({'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':jobname,'namespace':ns},'data':{'acceptance.py':(m.D/'acceptance.py').read_text()}})
 spec=m.podspec(m.TRAIN_IMAGE,'',[{'name':'input','secret':{'secretName':jobname,'defaultMode':288}},{'name':'code','configMap':{'name':jobname}},{'name':'tmp','emptyDir':{'sizeLimit':'256Mi'}}],[{'name':'input','mountPath':'/input','readOnly':True},{'name':'code','mountPath':'/code','readOnly':True},{'name':'tmp','mountPath':'/tmp'}]);spec['serviceAccountName']='verifier';spec['containers'][0]['command']=['/opt/venv/bin/python','-I','-B','/code/acceptance.py','/input/config.json'];spec['containers'][0]['resources']['limits']['memory']='1Gi'
 obj=m.job(jobname,ns,spec);obj['spec']['template']['metadata']['labels']['app']='cpu-p01-acceptance';create(obj);raw=waitjob(jobname,ns,'check');(m.D/(jobname+'.log')).write_bytes(raw);print(raw.decode())
def negatives(name):
 body=m.load(name+'.request.json');target=execution(name);checks=[]
 for title,change,status in [('same-key-changed-name',{'name':'manual-changed'},409),('wrong-epochs',{'general_parameters':[{'name':'epochs','type':'INTEGER','value':'4'}],'idempotency_key':str(uuid.uuid4())},400),('gpu-kind',{'kind':'GPU_TRAINING','idempotency_key':str(uuid.uuid4())},400)]:
  changed=copy.deepcopy(body);changed.update(change);r=call('POST','/executions',changed,(status,));checks.append({'test':title,**r})
 for who,status in [('b',404),('ungranted',403)]:checks.append({'test':who+'-execution',**call('GET','/executions/'+target,expected=(status,),who=who)})
 artifacts=call('GET','/executions/'+target+'/artifacts')['artifacts'];artifact=artifacts[0]['artifact_id']
 checks.append({'test':'b-artifact',**call('GET','/artifacts/'+artifact+'/content',expected=(404,),who='b')})
 request=urllib.request.Request(BASE+ROOT+'/executions/'+target)
 try:urllib.request.urlopen(request,timeout=10);raise RuntimeError('Unauthenticated request unexpectedly accepted')
 except urllib.error.HTTPError as e:assert e.code==401;checks.append({'test':'no-token','http_status':e.code});e.close()
 m.save(name+'.negative-checks.json',checks);print(json.dumps({'result':'PASS','checks':checks}))
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('command',choices=['login','catalogue','enable','run','wait','logs','verify','negatives','inspect','cleanup-plan','cleanup-apply']);p.add_argument('name',nargs='?');p.add_argument('value',nargs='?');a=p.parse_args()
 if a.command=='login':login()
 elif a.command=='catalogue':print(json.dumps({'presets':call('GET','/presets?page_size=100'),'inputs':call('GET','/input-versions?state=READY&page_size=100')}))
 elif a.command=='enable':enable(a.name)
 elif a.command=='run':run(a.name,a.value or 'success')
 elif a.command=='wait':wait(a.name,a.value or 'SUCCEEDED')
 elif a.command=='verify':verify(a.name)
 elif a.command=='negatives':negatives(a.name)
 elif a.command=='logs':print(json.dumps(call('GET','/executions/'+execution(a.name)+'/logs?tail_lines=100&max_bytes=32768')))
 else:
  request={'execution_id':execution(a.name)}
  if a.command=='cleanup-apply':request['plan_sha256']=m.load('manual-cleanup-plan-'+a.name+'.operation.json')['response']['plan_sha256']
  reply=manage('modeldev-'+a.command,'manual-'+a.command+'-'+a.name,request)
  if a.command=='cleanup-apply':assert reply['phase'] in ['APPLIED','RECONCILED']
if __name__=='__main__':
 try:main()
 except Exception as e:
  print('STOPPED: '+type(e).__name__+'; inspect the retained request, receipt and Job; do not blindly repeat mutations.',file=sys.stderr);sys.exit(1)
