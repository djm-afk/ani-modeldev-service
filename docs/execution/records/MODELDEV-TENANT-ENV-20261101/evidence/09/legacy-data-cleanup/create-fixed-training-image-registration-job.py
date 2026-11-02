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

requests=[{'kind': 'cpu', 'body': {'idempotency_key': 'modeldev-tenantenv-d807-20261101-cpu', 'image_reference': '10.10.1.66:30003/ani-resource-factor10-20261022/ani-modeldev-training-cpu@sha256:31227c3af9d3d0afe5cffddf34d6a3e0a922e29ea7e153892c8b0a0d1ed7f1d5', 'display_name': 'ModelDev Training CPU d807cbdb', 'description': 'Fixed ModelDev source d807cbdbd5dd6664d61480acedc752e19cfca48a; tenant environment implementation 20261101; immutable published digest.', 'purposes': ['training'], 'accelerator': 'none'}, 'body_byte_sha256': '54b820a14327dffff3159b5982f965b158907891f341d0e9e956a3f4abd96479', 'expected_digest': 'sha256:31227c3af9d3d0afe5cffddf34d6a3e0a922e29ea7e153892c8b0a0d1ed7f1d5'}, {'kind': 'gpu', 'body': {'idempotency_key': 'modeldev-tenantenv-d807-20261101-gpu', 'image_reference': '10.10.1.66:30003/ani-resource-factor10-20261022/ani-modeldev-training-gpu@sha256:aa1f6ae2159eead52d0d2343d53d785021aa4572dc9978e1af221eb30519eb6e', 'display_name': 'ModelDev Training GPU d807cbdb', 'description': 'Fixed ModelDev source d807cbdbd5dd6664d61480acedc752e19cfca48a; tenant environment implementation 20261101; immutable published digest.', 'purposes': ['training', 'finetuning'], 'accelerator': 'nvidia'}, 'body_byte_sha256': '82fd58776c400de83da6a5a739af1f68bc207919d53bbdf3d373f72c3d4fdac4', 'expected_digest': 'sha256:aa1f6ae2159eead52d0d2343d53d785021aa4572dc9978e1af221eb30519eb6e'}]
publication_result_byte_sha256='e35d2545ee9f54a9bd0849602f848f49cfec101612a141a92ff05b357e710205'

d=get(['-n','ani-system','get','deployment','ani-resource-service']);assert d['metadata']['uid']=='9b17ed7e-6521-40e8-8bf5-70a44237e501'
p=get(['-n','ani-system','get','pod','ani-resource-service-5d5bb7b9d9-6v4qb']);assert p['metadata']['uid']=='f6e8e746-1664-4544-8dca-ea05b53d7483' and p['spec']['nodeName']=='node3'
image='10.10.1.66:30003/ani-gpu-platform/ani-resource-service@sha256:71c04b8948728d8d0299320f148afa3fee3eabdbea87051c2b99c300abe730d1'
assert [c['image'] for c in p['spec']['containers'] if c['name']=='resource']==[image]
sa=get(['-n','ani-system','get','serviceaccount',p['spec']['serviceAccountName']]);assert sa['metadata']['name']=='ani-resource-service' and sa['metadata']['uid']=='b01307c9-f6e2-4122-b82a-dea120ed30f1'
assert s['metadata']['uid']=='54303da4-f97a-451d-b186-4d1a35d0c466' and s['metadata']['resourceVersion']=='20339931'
configsha=hashlib.sha256(base64.b64decode(s['data']['config.yaml'])).hexdigest();assert configsha=='87631172ea6104e430d302ef1885fbf0f8f11b6f929ef55bafcff87517ea15a3'
name='modeldev-training-image-register-20261101';cmname=name+'-operator'
for kind,n in [('job',name),('configmap',cmname)]:
 r=subprocess.run(['kubectl','--request-timeout=15s','-n','ani-system','get',kind,n,'--ignore-not-found','-o','json'],capture_output=True,timeout=20)
 assert r.returncode==0 and not r.stdout.strip(),'task object already exists; reconcile original IDs rather than repeat'
operator={'subject':'ssh:kubercloud@100.64.128.12','actor':'operator:modeldev-tenantenv-20261101','installation_id':'7e1334f4-5202-4819-8d9d-96248d3d4b00','secret_output_directory':'/private'}
data={'operator.json':json.dumps(operator,separators=(',',':'))}
for index,r in enumerate(requests):data[str(index)+'.json']=json.dumps(r['body'],ensure_ascii=False,separators=(',',':'))
cm={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':cmname,'namespace':'ani-system','labels':{'ani.io/task':'modeldev-tenantenv-20261101','ani.io/purpose':'fixed-two-training-image-registration'}},'immutable':True,'data':data}
files=['config.yaml','harbor-ca.crt','harbor.password','image-cursor.key','image-keyring.json','image-runtime.dsn']
initimage=p['spec']['initContainers'][0]['image'];assert initimage=='10.10.1.66:5001/library/busybox@sha256:7a3ebe5bfd1a4a19797d20b0c0bb39d44393e9a03fd852c0865b0f540d868df0'
initcmd='umask 077\necho "'+configsha+'  /input/config.yaml" | sha256sum -c - >/dev/null\nfor name in '+ ' '.join(files)+'; do cp -L /input/$name /private/$name; chmod 600 /private/$name; done\nfor name in operator.json 0.json 1.json; do cp -L /operator/$name /private/$name; chmod 600 /private/$name; done'
security={'allowPrivilegeEscalation':False,'capabilities':{'drop':['ALL']},'readOnlyRootFilesystem':True}
job={'apiVersion':'batch/v1','kind':'Job','metadata':{'name':name,'namespace':'ani-system','labels':{'ani.io/task':'modeldev-tenantenv-20261101','ani.io/purpose':'fixed-two-training-image-registration'}},'spec':{'completions':2,'parallelism':1,'completionMode':'Indexed','backoffLimit':0,'activeDeadlineSeconds':180,'template':{'metadata':{'labels':{'ani.io/task':'modeldev-tenantenv-20261101','ani.io/purpose':'fixed-two-training-image-registration'}},'spec':{'nodeName':'node3','restartPolicy':'Never','serviceAccountName':'ani-resource-service','automountServiceAccountToken':True,'securityContext':p['spec']['securityContext'],'imagePullSecrets':p['spec'].get('imagePullSecrets',[]),'initContainers':[{'name':'private-regular-files','image':initimage,'imagePullPolicy':'IfNotPresent','command':['sh','-ec',initcmd],'securityContext':security,'resources':{'requests':{'cpu':'25m','memory':'16Mi'},'limits':{'cpu':'200m','memory':'64Mi'}},'volumeMounts':[{'name':'input','mountPath':'/input','readOnly':True},{'name':'operator','mountPath':'/operator','readOnly':True},{'name':'private','mountPath':'/private'}]}],'containers':[{'name':'register-platform','image':image,'imagePullPolicy':'IfNotPresent','command':['/ani-resource-service'],'args':['-conf','/private/config.yaml','-image-admin','register-platform','-image-operator-config','/private/operator.json','-image-admin-input','/private/$(COMMAND_INDEX).json'],'env':[{'name':'COMMAND_INDEX','valueFrom':{'fieldRef':{'fieldPath':"metadata.annotations['batch.kubernetes.io/job-completion-index']"}}}],'securityContext':security,'resources':{'requests':{'cpu':'50m','memory':'64Mi'},'limits':{'cpu':'300m','memory':'256Mi'}},'volumeMounts':[{'name':'private','mountPath':'/private','readOnly':True}]}],'volumes':[{'name':'input','secret':{'secretName':'ani-resource-private','defaultMode':288,'items':[{'key':f,'path':f} for f in files]}},{'name':'operator','configMap':{'name':cmname,'defaultMode':288}},{'name':'private','emptyDir':{'medium':'Memory','sizeLimit':'4Mi'}}]}}}}
manifest={'apiVersion':'v1','kind':'List','items':[cm,job]}
raw=json.dumps(manifest,sort_keys=True,separators=(',',':')).encode()
root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
(root/'training-image-register-job-manifest.json').write_bytes(raw);(root/'training-image-register-job-manifest.json').chmod(0o600)
r=subprocess.run(['kubectl','--request-timeout=15s','create','-f','-','-o','json'],input=raw,capture_output=True,timeout=30)
(root/'training-image-register-job-create.stdout').write_bytes(r.stdout);(root/'training-image-register-job-create.stderr').write_bytes(r.stderr)
if r.returncode:raise RuntimeError('task Job/ConfigMap create failed; original stdout retained for partial reconciliation, do not repeat')
created=json.loads(r.stdout)
assert len(created['items'])==2
receipt={'schema':'ani.modeldev.training-image-registration-job.v1','observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'original':{'deployment_uid':d['metadata']['uid'],'deployment_spec_sha256':hashlib.sha256(json.dumps(d['spec'],sort_keys=True,separators=(',',':')).encode()).hexdigest(),'pod_name':p['metadata']['name'],'pod_uid':p['metadata']['uid'],'image':image,'service_account_name':sa['metadata']['name'],'service_account_uid':sa['metadata']['uid'],'secret_name':s['metadata']['name'],'secret_uid':s['metadata']['uid'],'secret_resource_version':s['metadata']['resourceVersion'],'config_sha256':configsha},'created':[{'kind':o['kind'],'name':o['metadata']['name'],'namespace':o['metadata']['namespace'],'uid':o['metadata']['uid']} for o in created['items']],'job_manifest_sha256':hashlib.sha256(raw).hexdigest(),'publication_result_byte_sha256':publication_result_byte_sha256,'requests':requests,'no_new_rbac_or_service_account':True,'status':'CREATED_AWAIT_ACTUAL_RESULTS'}
(root/'training-image-register-job-create.json').write_text(json.dumps(receipt,indent=2)+'\n');(root/'training-image-register-job-create.json').chmod(0o600)
print(json.dumps(receipt))
