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

p=get(['-n','ani-system','get','pod','ani-resource-service-5d5bb7b9d9-6v4qb']);assert p['metadata']['uid']=='f6e8e746-1664-4544-8dca-ea05b53d7483'
c=[v for v in p['status']['containerStatuses'] if v['name']=='resource'][0]['containerID'].removeprefix('containerd://')
r=subprocess.run(['sudo','-n','ctr','-n','k8s.io','tasks','ls'],capture_output=True,timeout=20);assert r.returncode==0,'existing containerd tasks read failed'
lines=[x.split() for x in r.stdout.decode().splitlines() if x.split() and x.split()[0]==c];assert len(lines)==1
pid=int(lines[0][1]);host='/var/lib/kubelet/pods/'+p['metadata']['uid']+'/volumes/kubernetes.io~empty-dir/private'
probe=r"""import os,stat,json,hashlib
from pathlib import Path
root=Path(HOST);mounted=Path('/proc/'+str(PID)+'/root/private')
a=root.stat();b=mounted.stat();assert (a.st_dev,a.st_ino)==(b.st_dev,b.st_ino),'host backing is not original mount'
mounts=[x.split() for x in Path('/proc/'+str(PID)+'/mountinfo').read_text().splitlines() if x.split()[4]=='/private'];assert len(mounts)==1 and 'ro' in mounts[0][5].split(',')
result={'host':HOST,'container_pid':PID,'host_mount_identity':{'device':a.st_dev,'inode':a.st_ino,'matched_original_container_private':True,'mode':oct(stat.S_IMODE(a.st_mode))},'mountinfo_private':mounts[0],'files':{}}
for name in ['config.yaml','operator-modeldev-tenantenv-1464b131-20261101.json']:
 f=root/name
 if not f.exists():result['files'][name]={'exists':False};continue
 s=f.lstat();result['files'][name]={'exists':True,'symlink':f.is_symlink(),'mode':oct(stat.S_IMODE(s.st_mode)),'uid':s.st_uid,'gid':s.st_gid,'inode':s.st_ino,'sha256':hashlib.sha256(f.read_bytes()).hexdigest()}
print(json.dumps(result))
"""
r=subprocess.run(['sudo','-n','python3','-'],input=('HOST='+repr(host)+'\nPID='+repr(pid)+'\n'+probe).encode(),capture_output=True,timeout=20);assert r.returncode==0,'exact original private backing proof failed'
print(json.dumps({'identity':actual,'pod_uid':p['metadata']['uid'],'container_id':c,'backing':json.loads(r.stdout),'current_secret_config_sha256':hashlib.sha256(base64.b64decode(s['data']['config.yaml'])).hexdigest()}))
