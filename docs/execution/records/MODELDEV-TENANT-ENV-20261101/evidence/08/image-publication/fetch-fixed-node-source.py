#!/usr/bin/env python3
"""Fetch only the manifest's fixed d807 ordinary files; anonymous TLS, no archives."""
from pathlib import Path
import concurrent.futures,hashlib,json,os,time,urllib.request,urllib.parse
ROOT=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
expected=json.loads((ROOT/'source-expected.json').read_text());assert expected['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'
SRC=ROOT/'source-d807';receipt=ROOT/'source-fetch-receipt.json';started=time.monotonic()
r={'status':'FETCHING','pid':os.getpid(),'source_commit':expected['source_commit'],'file_count':689,'archives_used':False,'authentication':'anonymous public immutable commit URL, default TLS validation','matched':0,'reused_chunk_parts':0,'fetched':0,'errors':[]}
def save():r['elapsed_seconds']=round(time.monotonic()-started,3);receipt.write_text(json.dumps(r,indent=2)+'\n')
save()
def fetch(item):
 name,v=item;dest=SRC/name
 def matches(p):return p.is_file() and not p.is_symlink() and p.stat().st_size==v['size'] and hashlib.sha256(p.read_bytes()).hexdigest()==v['sha256']
 if matches(dest):return 'matched',name
 part=dest.with_name(dest.name+'.transfer-part')
 if matches(part):os.replace(part,dest);return 'reused_chunk_parts',name
 url='https://raw.githubusercontent.com/zhangzhe-ctrl/ani-modeldev-service/'+expected['source_commit']+'/'+urllib.parse.quote(name,safe='/')
 for attempt in range(3):
  try:
   opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
   with opener.open(url,timeout=25) as response:
    assert response.status==200
    data=response.read(v['size']+1)
   assert len(data)==v['size'] and hashlib.sha256(data).hexdigest()==v['sha256'],'fixed source SHA differs'
   dest.parent.mkdir(parents=True,exist_ok=True);temp=dest.with_name(dest.name+'.raw-fetch-part')
   fd=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW,0o600)
   with os.fdopen(fd,'wb') as out:out.write(data)
   os.replace(temp,dest);return 'fetched',name
  except Exception as e:
   if attempt==2:return 'error',{'path':name,'error_class':type(e).__name__,'http_status':getattr(e,'code',None)}
with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
 for future in concurrent.futures.as_completed([pool.submit(fetch,item) for item in expected['files'].items()]):
  kind,value=future.result()
  if kind=='error':r['errors'].append(value)
  else:r[kind]+=1
  save()
if not r['errors']:
 actual={name:hashlib.sha256((SRC/name).read_bytes()).hexdigest() for name in expected['files']}
 assert actual=={n:v['sha256'] for n,v in expected['files'].items()}
 for name in expected['files']:
  part=(SRC/name).with_name(Path(name).name+'.transfer-part')
  if part.exists():assert not part.is_symlink();part.unlink()
 r.update(status='ALL_689_SHA_MATCH',verified_files=len(actual),fixed_manifest_canonical_sha256=expected['input_files_canonical_sha256'])
else:r['status']='FIXED_SOURCE_FETCH_FAILED'
save()
