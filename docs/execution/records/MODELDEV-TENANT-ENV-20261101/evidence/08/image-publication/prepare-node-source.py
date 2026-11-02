#!/usr/bin/env python3
"""Fixed manifest initialization and exact reuse from the known old Goal only."""
import base64,hashlib,json,shutil,zlib
from pathlib import Path
ROOT=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
i=identity()
data=zlib.decompress(base64.b64decode(MANIFEST_B64));assert hashlib.sha256(data).hexdigest()==MANIFEST_SHA
expected=json.loads(data);assert expected['file_count']==689 and expected['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'
src=ROOT/'source-d807';src.mkdir(mode=0o700,exist_ok=True)
(ROOT/'source-expected.json').write_bytes(data)
(ROOT/'source-identity.json').write_text(json.dumps({'identity':i,'source_commit':expected['source_commit']}))
old=Path('/home/kubercloud/modeldev-open-limits-image-build-20261029')
candidates=[]
entries={}
for n in ['platform','platform-current-f839f438ee14','platform-sft-runtime-final-20261029','qualification-builder']:
 p=old/n
 if p.is_dir():
  try:entries[str(p)]=[x.name for x in sorted(p.iterdir())][:60]
  except PermissionError:
   entries[str(p)]='EXISTING_PERMISSION_DENIED_SKIPPED_UNCHANGED';continue
  candidates.extend([p,p/'source'])
reused=[];present=[];missing=[]
for name,v in expected['files'].items():
 p=src/name
 if p.is_file() and not p.is_symlink() and hashlib.sha256(p.read_bytes()).hexdigest()==v['sha256']:present.append(name);continue
 found=False
 for candidate in candidates:
  oldfile=candidate/name
  try:matching=oldfile.is_file() and not oldfile.is_symlink() and oldfile.stat().st_size==v['size'] and hashlib.sha256(oldfile.read_bytes()).hexdigest()==v['sha256']
  except PermissionError:matching=False
  if matching:
   p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(oldfile,p);p.chmod(0o600);reused.append(name);found=True;break
 if not found:missing.append(name)
r={'identity':i,'expected_file_count':689,'already_present_count':len(present),'reused_count':len(reused),'missing':missing,'missing_bytes':sum(expected['files'][p]['size'] for p in missing),'old_candidate_entries':entries}
(ROOT/'source-transfer-needed.json').write_text(json.dumps(r,indent=2)+'\n')
print(json.dumps(r))
