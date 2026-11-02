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
root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
assert json.loads((root/'transfer-source.json').read_text())['source_commit']=='1464b131185791f71990b1cf864e1468186c8bd4'
pod='ani-resource-service-5d5bb7b9d9-6v4qb';poduid='f6e8e746-1664-4544-8dca-ea05b53d7483'
def execpod(args,body=None):
 r=subprocess.run(['kubectl','--request-timeout=15s','-n','ani-system','exec']+(['-i'] if body is not None else [])+[pod,'-c','resource','--']+args,input=body,capture_output=True,timeout=60)
 return r
p=get(['-n','ani-system','get','pod',pod]);assert p['metadata']['uid']==poduid and not p['metadata'].get('deletionTimestamp')
private='/private'
operator_path=private+'/operator-modeldev-tenantenv-1464b131-20261101.json'
operator={'subject':'ssh:kubercloud@100.64.128.12','actor':'operator:modeldev-tenantenv-20261101','installation_id':'7e1334f4-5202-4819-8d9d-96248d3d4b00','secret_output_directory':private}
# The original container is distroless/read-only. Add only this private
# capability file through the exact original emptyDir backing, never overwrite
# mounted source configuration or create a new execution identity.
host_root='/var/lib/kubelet/pods/'+poduid+'/volumes/kubernetes.io~empty-dir/private'
create=r"""
import os,stat,json,hashlib
root=HOST_ROOT
fd=os.open(root,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
try:
 st=os.stat('config.yaml',dir_fd=fd,follow_symlinks=False)
 assert stat.S_ISREG(st.st_mode) and st.st_uid==10001
 configfd=os.open('config.yaml',os.O_RDONLY|os.O_NOFOLLOW,dir_fd=fd)
 try:raw=os.read(configfd,1<<20)
 finally:os.close(configfd)
 assert hashlib.sha256(raw).hexdigest()==EXPECTED_CONFIG_SHA256,'mounted original config differs from reviewed source'
 new=os.open(NAME,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=fd)
 try:
  assert os.write(new,OPERATOR)==len(OPERATOR);os.fchown(new,10001,10001);os.fsync(new)
  st=os.fstat(new)
 finally:os.close(new)
 print(json.dumps({'path':root+'/'+NAME,'uid':st.st_uid,'gid':st.st_gid,'mode':oct(stat.S_IMODE(st.st_mode)),'inode':st.st_ino,'sha256':hashlib.sha256(OPERATOR).hexdigest(),'original_config_sha256':EXPECTED_CONFIG_SHA256}))
finally:os.close(fd)
"""
create='HOST_ROOT='+repr(host_root)+'\nEXPECTED_CONFIG_SHA256='+repr(hashlib.sha256(base64.b64decode(s['data']['config.yaml'])).hexdigest())+'\nNAME='+repr(Path(operator_path).name)+'\nOPERATOR='+repr(json.dumps(operator,separators=(',',':')).encode())+'\n'+create
r=subprocess.run(['sudo','python3','-'],input=create.encode(),capture_output=True,timeout=20)
assert r.returncode==0,'exact original private backing/capability creation failed'
operator_file=json.loads(r.stdout)
results=[]
for request in requests:
 p=get(['-n','ani-system','get','pod',pod]);assert p['metadata']['uid']==poduid and not p['metadata'].get('deletionTimestamp')
 # Recheck full identity and original source configuration before each catalog write.
 assert run(['kubectl','config','current-context']).decode().strip()==actual['context']
 assert get(['get','ns','kube-system'])['metadata']['uid']==actual['kube_system_uid']
 current=get(['-n','ani-system','get','secret','ani-resource-private'])
 assert hashlib.sha256(base64.b64decode(current['data']['config.yaml'])).hexdigest()==hashlib.sha256(base64.b64decode(s['data']['config.yaml'])).hexdigest()
 body=json.dumps(request['body'],ensure_ascii=False,separators=(',',':')).encode();assert hashlib.sha256(body).hexdigest()==request['body_byte_sha256']
 args=['/ani-resource-service','-conf','/private/config.yaml','-image-admin','register-platform','-image-operator-config',operator_path]
 r=execpod(args,body)
 rawpath=root/('platform-'+request['kind']+'-register.stdout');rawpath.write_bytes(r.stdout);rawpath.chmod(0o600)
 errpath=root/('platform-'+request['kind']+'-register.stderr');errpath.write_bytes(r.stderr);errpath.chmod(0o600)
 if r.returncode:raise RuntimeError('register-platform '+request['kind']+' failed exit '+str(r.returncode)+'; raw output retained only on node')
 values=[]
 for line in r.stdout.splitlines():
  try:val=json.loads(line)
  except Exception:continue
  if isinstance(val,dict) and val.get('action')=='register-platform':values.append(val)
 assert len(values)==1,'unexpected operator receipt shape'
 value=values[0];registration=value['result']
 assert registration['Scope']=='platform' and registration['TenantID']=='' and registration['Digest']==request['expected_digest'] and registration['Version']>=1
 assert registration['ResolvedReference']==request['body']['image_reference'] and registration['Metadata']['Accelerator']==request['body']['accelerator'] and set(registration['Metadata']['Purposes'])==set(request['body']['purposes'])
 assert any(p['OS']=='linux' and p['Architecture']=='amd64' for p in registration['Platforms'])
 results.append({'kind':request['kind'],'request':request['body'],'request_byte_sha256':request['body_byte_sha256'],'operator_stdout_byte_sha256':hashlib.sha256(r.stdout).hexdigest(),'exit':r.returncode,'receipt':value})
print(json.dumps({'schema':'ani.modeldev.training-platform-image-registration.v1','observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'identity':actual,'resource_pod_uid':poduid,'resource_image_digest':'sha256:71c04b8948728d8d0299320f148afa3fee3eabdbea87051c2b99c300abe730d1','publication_result_byte_sha256':publication_result_byte_sha256,'operator':operator,'operator_file':operator_file,'results':results,'business_acceptance':'NOT_RUN_CATALOG_REGISTRATION_ONLY'}))
