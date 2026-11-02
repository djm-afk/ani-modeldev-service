#!/usr/bin/env python3
"""Existing fixed binary, <=1MiB request payload, original offsets and full SHA."""
import argparse,base64,hashlib,json,subprocess,time,zlib
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--socket',required=True);a=p.parse_args()
BASE=Path(__file__).resolve().parent;WORK=BASE.parents[4]
LOCAL=Path('/home/chabking/workspace/.scratch/legacy-cleanup-1464b131/original-c99-qualifier')
EXPECTED='a1891a0e388e8fe4a6280e4803c9b2d068a8317908739dabeb2e0036689eb78d';SIZE=25374676
binary=LOCAL.read_bytes();assert len(binary)==SIZE and hashlib.sha256(binary).hexdigest()==EXPECTED
SSH=['ssh','-S',a.socket,'-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-']
common=r'''
import os,json,hashlib,stat,zlib,base64
from pathlib import Path
root=Path('/home/kubercloud/tenantenv-20261101/legacy-cleanup-1464b131')
assert json.loads((root/'transfer-source.json').read_text())['source_commit']=='1464b131185791f71990b1cf864e1468186c8bd4'
expected='a1891a0e388e8fe4a6280e4803c9b2d068a8317908739dabeb2e0036689eb78d';size=25374676
dest=root/'original-c99-qualifier';part=dest.with_name(dest.name+'.fixed-transfer-part')
'''
records=[]
for offset in range(0,len(binary),524288):
 data=binary[offset:offset+524288]
 records.append({'offset':offset,'size':len(data),'sha256':hashlib.sha256(data).hexdigest(),'data':base64.b64encode(zlib.compress(data,9)).decode()})
descriptors=[{k:v for k,v in x.items() if k!='data'} for x in records]
probe=common+'\ndescriptors='+repr(descriptors)+r'''
have=[];complete=dest.is_file() and not dest.is_symlink() and dest.stat().st_size==size and hashlib.sha256(dest.read_bytes()).hexdigest()==expected
if part.is_file() and not part.is_symlink():
 with part.open('rb') as f:
  for r in descriptors:
   f.seek(r['offset']);data=f.read(r['size'])
   if hashlib.sha256(data).hexdigest()==r['sha256']:have.append(r['offset'])
print(json.dumps({'complete':complete,'matching_part_offsets':have,'source_fetch':json.loads((root/'source-fetch-receipt.json').read_text()) if (root/'source-fetch-receipt.json').exists() else None}))
'''
t=time.monotonic();probe_result=subprocess.run(SSH,input=probe.encode(),capture_output=True,timeout=45);assert probe_result.returncode==0,(probe_result.returncode,probe_result.stderr.decode());resume=json.loads(probe_result.stdout)
records=[] if resume['complete'] else [x for x in records if x['offset'] not in resume['matching_part_offsets']]
writer=common+r'''
dest.parent.mkdir(parents=True,exist_ok=True)
fd=os.open(part,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
try:
 assert stat.S_ISREG(os.fstat(fd).st_mode)
 if os.fstat(fd).st_size!=size:os.ftruncate(fd,size)
 for r in records:
  data=zlib.decompress(base64.b64decode(r['data']))
  assert len(data)==r['size'] and hashlib.sha256(data).hexdigest()==r['sha256']
  assert 0<=r['offset']<r['offset']+len(data)<=size
  assert os.pwrite(fd,data,r['offset'])==len(data)
finally:os.close(fd)
print(json.dumps({'written':len(records)}))
'''
batches=[];current=[];wire=0
for r in records:
 amount=len(repr(r).encode())
 if current and wire+amount>1040000:batches.append(current);current=[];wire=0
 current.append(r);wire+=amount
if current:batches.append(current)
receipt={'source':'Fixed source1464b131 Fedora-built legacy cleanup tool; ordinary compressed fixed-offset file transfer only, NOT_EXECUTED','source_sha256':EXPECTED,'source_bytes':SIZE,'resume':resume,'batch_count':len(batches),'max_request_bytes':1048576,'batches':[],'status':'TRANSFERRING','root_owns_socket':True}
out=BASE/'node-original-c99-qualifier-chunk-transfer.json'
def save():receipt['elapsed_seconds']=round(time.monotonic()-t,3);out.write_text(json.dumps(receipt,indent=2)+'\n')
save()
for index,batch in enumerate(batches):
 payload=('records='+repr(batch)+'\n'+writer).encode();assert len(payload)<=1048576
 started=time.monotonic();result=subprocess.run(SSH,input=payload,capture_output=True,timeout=90)
 item={'batch':index,'payload_bytes':len(payload),'offsets':[r['offset'] for r in batch],'exit':result.returncode,'seconds':round(time.monotonic()-started,3)}
 receipt['batches'].append(item)
 if result.returncode:receipt['status']='SSH_FAILED_RESUME_EXACT_OFFSETS';item['stderr_sha256']=hashlib.sha256(result.stderr).hexdigest();save();raise SystemExit(1)
 assert json.loads(result.stdout)['written']==len(batch);save()
 print(json.dumps({'batch':index+1,'total':len(batches),'elapsed_seconds':receipt['elapsed_seconds']}),flush=True)
final=common+r'''
if part.exists():
 assert part.is_file() and not part.is_symlink() and part.stat().st_size==size
 actual=hashlib.sha256(part.read_bytes()).hexdigest();assert actual==expected
 part.chmod(0o700);os.replace(part,dest)
assert dest.stat().st_size==size and hashlib.sha256(dest.read_bytes()).hexdigest()==expected
dest.chmod(0o700)
assert stat.S_IMODE(dest.stat().st_mode)==0o700
print(json.dumps({'path':str(dest),'size':size,'sha256':expected,'all_bytes_verified':True,'mode':'0700','executed':False}))
'''
r=subprocess.run(SSH,input=final.encode(),capture_output=True,timeout=45);assert r.returncode==0,(r.returncode,r.stderr.decode());receipt['final']=json.loads(r.stdout);receipt['status']='FULL_BINARY_SHA_MATCH';save();print(json.dumps(receipt['final']))
