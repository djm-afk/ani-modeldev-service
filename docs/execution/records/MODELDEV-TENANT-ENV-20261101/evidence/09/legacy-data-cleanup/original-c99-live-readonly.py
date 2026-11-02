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

root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
assert json.loads((root/'transfer-source.json').read_text())['source_commit']=='1464b131185791f71990b1cf864e1468186c8bd4'
binary=root/'original-c99-qualifier';assert binary.is_file() and not binary.is_symlink() and hashlib.sha256(binary.read_bytes()).hexdigest()=='a1891a0e388e8fe4a6280e4803c9b2d068a8317908739dabeb2e0036689eb78d'
r=subprocess.run(['sudo','-n',str(binary),'-dsn-file','/tmp/gov-original-command-retirement-ca01cabf-20261101/modeldev-readonly.dsn'],capture_output=True,timeout=120)
if r.returncode:
 print(json.dumps({'identity':actual,'exit':r.returncode,'finite_stderr':r.stderr.decode()[:2000],'readonly':True}));raise SystemExit(r.returncode)
proof=json.loads(r.stdout);assert proof['CodecCommit']=='c99c9f1081c754f7e38ceccd056db9a032ca3c27' and len(proof['Records'])==7
path=root/'original-c99-live-qualification.json';path.write_bytes(r.stdout);path.chmod(0o600)
print(json.dumps({'schema':'ani.modeldev.original-c99-live-readonly.v1','identity':actual,'readonly':True,'exit':r.returncode,'proof_path':str(path),'proof_byte_sha256':hashlib.sha256(r.stdout).hexdigest(),'qualification':proof,'business_mutation':False}))
