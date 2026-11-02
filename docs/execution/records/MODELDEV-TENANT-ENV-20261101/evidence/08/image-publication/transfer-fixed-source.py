#!/usr/bin/env python3
"""One SSH channel, bounded fixed-offset file chunks; final whole-file SHA required."""
import argparse,base64,hashlib,json,subprocess,time,zlib
from pathlib import Path
P=argparse.ArgumentParser();P.add_argument('--socket',required=True);a=P.parse_args()
BASE=Path(__file__).resolve().parent
WORK=BASE.parents[4]
LOCAL=WORK/'.scratch/modeldev-node-source-d807'
ROOT='/home/kubercloud/tenantenv-20261101/images-d807-build-20261101'
expected=json.loads((LOCAL/'source-expected.json').read_text())
needed_path=BASE/'node-source-remaining.json'
needed=json.loads((needed_path if needed_path.exists() else BASE/'node-source-reuse.json').read_text())['missing']
SSH=['ssh','-S',a.socket,'-o','BatchMode=yes','-o','ProxyCommand=false','kubercloud@100.64.128.12','python3','-']
WRITER=r'''
import os,json,zlib,base64,hashlib,stat
from pathlib import Path
root=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
expected=json.loads((root/'source-expected.json').read_text())
assert expected['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'
for record in records:
 name=record['path'];assert name in expected['files'] and '..' not in Path(name).parts
 value=expected['files'][name];data=zlib.decompress(base64.b64decode(record['data']))
 assert len(data)==record['size']<=32768 and hashlib.sha256(data).hexdigest()==record['sha256']
 assert 0<=record['offset']<=record['offset']+len(data)<=value['size']
 dest=root/'source-d807'/name;dest.parent.mkdir(parents=True,exist_ok=True)
 part=dest.with_name(dest.name+'.transfer-part')
 fd=os.open(part,os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
 try:
  assert stat.S_ISREG(os.fstat(fd).st_mode)
  if os.fstat(fd).st_size!=value['size']:os.ftruncate(fd,value['size'])
  assert os.pwrite(fd,data,record['offset'])==len(data)
 finally:os.close(fd)
print(json.dumps({'records_written':len(records)}))
'''
records=[]
for name in needed:
 data=(LOCAL/name).read_bytes();assert hashlib.sha256(data).hexdigest()==expected['files'][name]['sha256']
 for offset in range(0,max(len(data),1),32768):
  chunk=data[offset:offset+32768]
  records.append({'path':name,'offset':offset,'size':len(chunk),'sha256':hashlib.sha256(chunk).hexdigest(),'data':base64.b64encode(zlib.compress(chunk,6)).decode()})
batches=[];current=[];size=0
for r in records:
 wire=len(repr(r).encode())
 if current and size+wire>1040000:batches.append(current);current=[];size=0
 current.append(r);size+=wire
if current:batches.append(current)
receipt={'status':'TRANSFERRING','socket':a.socket,'root_owns_socket':True,'file_count':len(needed),'batch_count':len(batches),'total_raw_bytes':sum(expected['files'][n]['size'] for n in needed),'chunks':len(records),'batches':[]}
receipt_path=BASE/'node-source-chunk-transfer.json';started=time.monotonic()
def save():receipt['elapsed_seconds']=round(time.monotonic()-started,3);receipt_path.write_text(json.dumps(receipt,indent=2)+'\n')
save()
for index,batch in enumerate(batches):
 payload=('records='+repr(batch)+'\n'+WRITER).encode();t=time.monotonic()
 result=subprocess.run(SSH,input=payload,capture_output=True,timeout=45)
 item={'batch':index,'payload_bytes':len(payload),'records':len(batch),'exit':result.returncode,'seconds':round(time.monotonic()-t,3)}
 if result.returncode:item['stderr_sha256']=hashlib.sha256(result.stderr).hexdigest();receipt['status']='SSH_FAILED_RESUME_SAME_FIXED_OFFSETS';receipt['batches'].append(item);save();raise SystemExit(1)
 assert json.loads(result.stdout)['records_written']==len(batch)
 receipt['batches'].append(item);save()
 print(json.dumps({'batch':index+1,'total':len(batches),'elapsed_seconds':receipt['elapsed_seconds']}),flush=True)
receipt['status']='CHUNKS_TRANSFERRED_PENDING_FULL_SHA';save()
