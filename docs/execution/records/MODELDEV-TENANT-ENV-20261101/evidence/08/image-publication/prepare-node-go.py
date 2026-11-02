#!/usr/bin/env python3
"""Necessary private Go toolchain for bit-identical d807 node compilation."""
from pathlib import Path
import hashlib,json,os,subprocess,tarfile,time,urllib.request
ROOT=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
TARGET_GO_SHA='9c2a37ae5b21ba1d5a004bb9ab4d0c64de6fc5778e9d30487adccea62678b8b5'
receipt=ROOT/'go-prepare-receipt.json'
r={'status':'STARTING','pid':os.getpid(),'version':'go1.26.10','private_tool_root':str(ROOT/'go-tools'),'cached_original_tools_modified':False}
def save():receipt.write_text(json.dumps(r,indent=2)+'\n')
def sha(p):return hashlib.file_digest(p.open('rb'),'sha256').hexdigest()
try:
    save()
    original=[Path('/usr/local/go/bin/go'),Path('/usr/bin/go'),Path('/home/kubercloud/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.linux-amd64/bin/go')]
    reused=None
    for p in original:
        if p.is_file() and sha(p)==TARGET_GO_SHA:reused=p;break
    p=subprocess.run(['sudo','-n','ctr','-n','k8s.io','images','ls','-q'],capture_output=True,text=True)
    r['cached_golang_images']=[s for s in p.stdout.splitlines() if 'golang' in s.lower() or 'go-toolchain' in s.lower()]
    if reused:
        go=reused;r['original_tool_reused']=True
    else:
        assert not any('1.26.10' in s for s in r['cached_golang_images']),'exact cached Go image found; reuse original image instead of duplicate download'
        root=ROOT/'go-tools';root.mkdir(mode=0o700,exist_ok=True)
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open('https://go.dev/dl/?mode=json&include=all',timeout=25) as response:metadata=json.load(response)
        release=next(x for x in metadata if x['version']=='go1.26.10')
        archive_meta=next(x for x in release['files'] if x['os']=='linux' and x['arch']=='amd64' and x['kind']=='archive')
        r['official_archive']=archive_meta;r['official_metadata_url']='https://go.dev/dl/?mode=json&include=all';save()
        archive=root/archive_meta['filename'];expected=archive_meta['sha256']
        if archive.exists() and (archive.stat().st_size!=archive_meta['size'] or sha(archive)!=expected):raise RuntimeError('existing private tool archive differs; inspect before replacement')
        started=time.monotonic()
        if not archive.exists():
            with opener.open('https://go.dev/dl/'+archive_meta['filename'],timeout=30) as source,archive.open('xb') as dest:
                while block:=source.read(1024*1024):dest.write(block)
        assert archive.stat().st_size==archive_meta['size'] and sha(archive)==expected,'official Go archive SHA mismatch'
        r['download_seconds']=time.monotonic()-started
        with tarfile.open(archive,'r:gz') as tf:
            for member in tf:
                path=Path(member.name)
                assert path.parts[0]=='go' and '..' not in path.parts and not path.is_absolute()
                target=root/path
                if member.isdir():target.mkdir(parents=True,exist_ok=True)
                elif member.isfile():
                    target.parent.mkdir(parents=True,exist_ok=True)
                    with tf.extractfile(member) as source,target.open('wb') as dest:
                        while block:=source.read(1024*1024):dest.write(block)
                    target.chmod(member.mode & 0o777)
                else:raise RuntimeError('unexpected nonregular official tool archive member')
        go=root/'go/bin/go';r['original_tool_reused']=False
    actual=sha(go);assert actual==TARGET_GO_SHA,'Go binary differs from verified Fedora compiler'
    r['go_path']=str(go);r['go_sha256']=actual
    p=subprocess.run([str(go),'version'],capture_output=True,text=True,check=True);r['go_version']=p.stdout.strip();assert r['go_version']=='go version go1.26.10 linux/amd64'
    r['status']='READY'
except Exception as e:r.update(status='FAILED',error_class=type(e).__name__,reason=str(e))
finally:save()
