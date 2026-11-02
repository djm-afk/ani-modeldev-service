#!/usr/bin/env python3
"""Remove only this task's stopped private build root; preserve shared caches/images."""
import hashlib,json,subprocess
from pathlib import Path
BASE=Path(__file__).resolve().parent
SOCKET='/home/chabking/workspace/.evidence/modeldev-node-control-20261101-v2/node.sock'
ROOT='/home/kubercloud/tenantenv-20261101/images-d807-build-20261101'
expected={n:hashlib.sha256((BASE/('node-'+n)).read_bytes()).hexdigest() for n in ('receipt.json','small-receipt.json')}
inner=r'''
import os,stat,json,hashlib,subprocess,shutil,time
from pathlib import Path
root=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
assert root.resolve()==root and root.is_dir() and not root.is_symlink()
assert json.loads((root/'source-identity.json').read_text())['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'
for name,sha in expected.items():
 p=root/name;assert p.is_file() and not p.is_symlink() and hashlib.sha256(p.read_bytes()).hexdigest()==sha
 assert json.loads(p.read_text())['status']=='PASS'
units={}
for unit in ('ani-modeldev-d807-build.service','ani-modeldev-d807-runner.service','ani-modeldev-d807-small-build.service','ani-modeldev-d807-small-runner.service'):
 p=subprocess.run(['systemctl','show',unit,'-p','MainPID','-p','ActiveState','-p','LoadState'],capture_output=True)
 fields=dict(x.split('=',1) for x in p.stdout.decode().splitlines() if '=' in x)
 assert fields.get('MainPID')=='0' and fields.get('ActiveState')=='inactive',(unit,fields)
 units[unit]=fields
mounts=[]
for line in Path('/proc/self/mountinfo').read_text().splitlines():
 mp=line.split()[4].replace('\\040',' ').replace('\\134','\\')
 if mp==str(root) or mp.startswith(str(root)+'/'):mounts.append(mp)
assert not mounts,('task mounts remain',mounts)
active=[]
for proc in Path('/proc').iterdir():
 if not proc.name.isdigit():continue
 try:cmd=(proc/'cmdline').read_bytes()
 except (FileNotFoundError,PermissionError,ProcessLookupError):continue
 if str(root).encode() in cmd:active.append(int(proc.name))
assert not active,('task processes still reference private root',active)
assert not (root/'buildkitd.sock').exists() and not (root/'auth').exists() and not (root/'small-auth').exists()
original=Path('/proc/2756988/exe')
original_before=os.readlink(original)
assert original_before=='/home/kubercloud/modeldev-open-limits-image-build-20261029/tools/buildkitd'
subpaths={name:(root/name).exists() for name in ('tools','state','context','small-context','source-d807','go-tools','go-mod-cache','go-build-cache','go-build-tmp','auth','small-auth','buildkitd.sock','ca.crt')}
files=0;total=0
for d,ds,fs in os.walk(root,followlinks=False):
 for n in fs:
  p=Path(d)/n;files+=1
  if not p.is_symlink():total+=p.stat().st_size
before={'path':str(root),'file_count':files,'bytes':total,'unit_states':units,'mounts':mounts,'processes_referencing_root':active,'socket_present':False,'auth_present':False,'subpaths_existed':subpaths,'verified_receipt_sha256':expected,'original_buildkit_exe':original_before}
shutil.rmtree(root)
assert not root.exists() and not root.is_symlink()
assert os.readlink(original)==original_before
ctr=subprocess.run(['ctr','-n','k8s.io','tasks','ls','-q'],check=True,capture_output=True)
print(json.dumps({'status':'EXACT_TASK_BUILD_ROOT_REMOVED','before':before,'after':{'root_absent':True,'original_buildkit_pid2756988_retained':True,'tasks':ctr.stdout.decode().splitlines()},'preserved':'All shared containerd image/content/cache metadata; all delivered registry images; original tools/daemon; root-owned SSH ControlMaster','no_prune_no_gc_no_shared_configuration_changes':True}))
'''
payload="__name__='cleanup_library'\n"+(BASE.parents[1]/'09/workbench-maintenance-job.py').read_text()+"\nchecked=identity()\nimport subprocess,json\ncode="+repr('expected='+repr(expected)+'\n'+inner)+"\np=subprocess.run(['sudo','-n','python3','-'],input=code.encode(),capture_output=True)\nassert p.returncode==0,(p.returncode,p.stderr.decode())\nresult=json.loads(p.stdout);result['identity']=checked\nprint(json.dumps(result))\n"
r=subprocess.run(['ssh','-S',SOCKET,'-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-'],input=payload.encode(),capture_output=True,timeout=90)
if r.returncode:
 out={'status':'CLEANUP_REFUSED_OR_UNKNOWN','ssh_exit':r.returncode,'stderr':r.stderr.decode()[-2000:]};(BASE/'node-private-build-cleanup.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps(out));raise SystemExit(1)
out=json.loads(r.stdout)
original=set(json.loads((BASE/'node-receipt.json').read_text())['tasks_before']);out['original_tasks_missing']=sorted(original-set(out['after']['tasks']))
out['original_task_preservation']='PASS' if not out['original_tasks_missing'] else 'OBSERVED_MISSING_REQUIRES_PARENT_RECONCILIATION'
(BASE/'node-private-build-cleanup.json').write_text(json.dumps(out,indent=2)+'\n');print(json.dumps({'status':out['status'],'bytes_removed':out['before']['bytes'],'original_tasks_missing':out['original_tasks_missing'],'original_buildkit_retained':out['after']['original_buildkit_pid2756988_retained']}))
