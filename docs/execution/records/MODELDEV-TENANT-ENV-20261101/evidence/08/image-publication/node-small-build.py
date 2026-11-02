#!/usr/bin/env python3
"""Fixed d807 small runtime images; original registry credentials remain node-local."""
import base64, concurrent.futures, contextlib, hashlib, io, json, os, re, shutil, ssl, subprocess, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path

ROOT=Path('/home/kubercloud/tenantenv-20261101/images-d807-build-20261101')
AUTHORITY='10.10.1.66:30003'
PROJECT='ani-resource-factor10-20261022'
BASE_DIGEST='sha256:eb5e106d93e47532e4b72f5995232624b704edea1c1c79468ddcd2ceffa14b4b'
BASE_REF=AUTHORITY+'/'+PROJECT+'/llamafactory@'+BASE_DIGEST
TAG='tenantenv-d807cbdbd5dd-node-20261101-8f6ba4'
UNIT='ani-modeldev-d807-small-build.service'
RECEIPT=ROOT/'small-receipt.json'
result={'schema':'ani.modeldev.node-image-build.v1','status':'STARTING','registry_push_issued':False,'resource_limits':'USER_REMOVED','images':{},'cleanup':{}}

def save():
    p=ROOT/'small-receipt.json.tmp';p.write_text(json.dumps(result,indent=2)+'\n');p.replace(RECEIPT)

def run(args,**kw):
    return subprocess.run(args,check=True,capture_output=True,**kw).stdout

def ctr(*args):
    return run(['sudo','-n','ctr','-n','k8s.io',*args])

def health():
    return [{'name':x['metadata']['name'],'uid':x['metadata']['uid'],'conditions':[{k:c[k] for k in ('type','status','reason') if k in c} for c in x['status']['conditions'] if c['type'] in ('Ready','DiskPressure','MemoryPressure','PIDPressure')]} for x in kget('get','nodes')['items']]

def main():
    result['identity']=identity()  # Full target identity before any task writes.
    context=json.loads((ROOT/'small-context/build-context.json').read_text())
    assert context['source_commit']=='d807cbdbd5dd6664d61480acedc752e19cfca48a'
    for name,v in context['files'].items():
        p=ROOT/'small-context'/name
        assert p.is_file() and not p.is_symlink() and p.stat().st_size==v['size']
        assert hashlib.sha256(p.read_bytes()).hexdigest()==v['sha256'],name
    tool_manifest=json.loads((ROOT/'tool-receipt.json').read_text())
    for f in tool_manifest['files']:
        p=ROOT/'tools'/f['name'];assert hashlib.sha256(p.read_bytes()).hexdigest()==f['sha256']
    result['source']=context;result['tools']=tool_manifest
    result['tasks_before']=ctr('tasks','ls','-q').decode().splitlines()
    result['nodes_before']=health()
    result['disk_before']=dict(zip(('total','used','free'),shutil.disk_usage(ROOT)))
    result['base']={'reference':'scratch','new_base_download_bytes':0}
    # Reuse the exact currently mounted original Resource credential projection.
    capture=io.StringIO()
    with contextlib.redirect_stdout(capture):exec(REGISTRY_READ,globals())
    credential=json.loads(capture.getvalue());result['registry_source']=credential['public_source']
    ca=base64.b64decode(credential['ca_pem_b64']);ca_path=ROOT/'ca.crt';ca_path.write_bytes(ca);ca_path.chmod(0o600)
    auth=ROOT/'small-auth';auth.mkdir(mode=0o700)
    userpass=(credential['username']+':'+credential['password']).encode()
    (auth/'config.json').write_text(json.dumps({'auths':{AUTHORITY:{'auth':base64.b64encode(userpass).decode()}}}));(auth/'config.json').chmod(0o600)
    class FixedRegistryRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,req,fp,code,msg,headers,newurl):
            target=urllib.parse.urlsplit(newurl)
            if target.scheme!='https' or target.netloc!=AUTHORITY:raise RuntimeError('registry cross-authority redirect refused')
            return super().redirect_request(req,fp,code,msg,headers,newurl)
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),FixedRegistryRedirect(),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca_path))))
    basic='Basic '+base64.b64encode(userpass).decode()
    def http(path,authorization=basic,headers=None):
        req=urllib.request.Request('https://'+AUTHORITY+path,headers={'Authorization':authorization,**(headers or {})})
        try:
            with opener.open(req,timeout=30) as r:return r.status,dict(r.headers),r.read()
        except urllib.error.HTTPError as e:return e.code,dict(e.headers),e.read()
    pre={}
    for mode in ('step','webhook'):
        repo='ani-modeldev-'+mode
        status,_,_=http('/api/v2.0/projects/'+PROJECT+'/repositories/'+repo+'/artifacts/'+TAG)
        assert status==404,('unique registry tag unavailable',mode,status)
        pre[mode]={'tag_absent_http':status}
    result['registry_preflight']=pre
    conf=f'''root = "{ROOT}/state"
[grpc]
address = ["unix://{ROOT}/buildkitd.sock"]
[worker.oci]
enabled = false
[worker.containerd]
enabled = true
address = "/run/containerd/containerd.sock"
namespace = "k8s.io"
platforms = ["linux/amd64"]
snapshotter = "overlayfs"
gc = false
networkMode = "host"
[cdi]
disabled = true
[registry."{AUTHORITY}"]
ca = ["{ca_path}"]
'''
    (ROOT/'buildkitd.toml').write_text(conf)
    run(['sudo','-n','systemd-run','--unit='+UNIT,'--collect','--property=StandardOutput=append:'+str(ROOT/'small-daemon.log'),'--property=StandardError=append:'+str(ROOT/'small-daemon.log'),str(ROOT/'tools/buildkitd'),'--config',str(ROOT/'buildkitd.toml')])
    result['daemon_started']=True
    for _ in range(60):
        if (ROOT/'buildkitd.sock').exists():break
        time.sleep(1)
    assert (ROOT/'buildkitd.sock').exists(),'BuildKit socket not ready'
    ctl=['sudo','-n','env','DOCKER_CONFIG='+str(auth),'SSL_CERT_FILE='+str(ca_path),str(ROOT/'tools/buildctl'),'--addr','unix://'+str(ROOT/'buildkitd.sock')]
    result['workers']=json.loads(run(ctl+['debug','workers','--format','{{json .}}']).decode())
    result['unit_limits']=run(['systemctl','show',UNIT,'-p','CPUQuotaPerSecUSec','-p','MemoryMax','-p','ControlGroup','-p','MainPID']).decode()
    result['status']='BUILDING';save()
    def build(mode):
        image=AUTHORITY+'/'+PROJECT+'/ani-modeldev-'+mode+':'+TAG
        metadata=ROOT/('small-'+mode+'-metadata.json');log=ROOT/('small-'+mode+'-build.log')
        args=ctl+['build','--frontend','dockerfile.v0','--local','context='+str(ROOT/'small-context'/mode),'--local','dockerfile='+str(ROOT/'small-context'/mode),'--opt','filename=Dockerfile','--opt','platform=linux/amd64','--opt','image-resolve-mode=local','--opt','force-network-mode=none','--opt','build-arg:SOURCE_REVISION='+context['source_commit'],'--opt','build-arg:SOURCE_TREE='+context['source_git_tree'],'--opt','label:org.opencontainers.image.revision='+context['source_commit'],'--opt','label:ani.modeldev.source_tree='+context['source_git_tree'],'--opt','label:ani.modeldev.build_inputs_sha256='+context['input_files_canonical_sha256'],'--output','type=image,name='+image+',push=true,store=true,unpack=true,oci-mediatypes=true','--metadata-file',str(metadata),'--progress','plain']
        started=time.monotonic();result['registry_push_issued']=True
        with log.open('wb') as f:p=subprocess.run(args,stdout=f,stderr=subprocess.STDOUT)
        entry={'image':image,'exit':p.returncode,'elapsed_seconds':round(time.monotonic()-started,3),'log_bytes':log.stat().st_size,'log_sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'command':args}
        if metadata.exists():entry['metadata']=json.loads(metadata.read_text())
        return mode,entry
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
        for future in concurrent.futures.as_completed([executor.submit(build,m) for m in ('step','webhook')]):
            mode,entry=future.result();result['images'][mode]=entry;save()
    for mode,entry in result['images'].items():
        if entry['exit']!=0:continue
        repo=PROJECT+'/ani-modeldev-'+mode
        status,headers,raw=http('/v2/'+repo+'/manifests/'+TAG,headers={'Accept':'application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'})
        authorization=basic
        if status==401:
            challenge=next(v for k,v in headers.items() if k.lower()=='www-authenticate')
            values=dict(re.findall(r'(\w+)="([^"]*)"',challenge));realm=urllib.parse.urlsplit(values['realm'])
            assert realm.scheme=='https' and realm.netloc==AUTHORITY
            token_path=realm.path+'?'+urllib.parse.urlencode({'service':values.get('service','harbor-registry'),'scope':'repository:'+repo+':pull'})
            st,_,body=http(token_path);assert st==200
            token_json=json.loads(body);authorization='Bearer '+(token_json.get('token') or token_json['access_token'])
            status,headers,raw=http('/v2/'+repo+'/manifests/'+TAG,authorization,{'Accept':'application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'})
        assert status==200,('manifest read',mode,status)
        digest='sha256:'+hashlib.sha256(raw).hexdigest()
        assert digest==entry['metadata']['containerimage.digest']
        image_manifest=json.loads(raw)
        cfg_raw=ctr('content','get',image_manifest['config']['digest'])
        assert 'sha256:'+hashlib.sha256(cfg_raw).hexdigest()==image_manifest['config']['digest']
        cfg=json.loads(cfg_raw);assert cfg['config']['Labels']['org.opencontainers.image.revision']==context['source_commit']
        assert cfg['config']['User']=='10001:10001'
        entry['registry']={'digest':digest,'immutable_image':AUTHORITY+'/'+repo+'@'+digest,'manifest':image_manifest,'config':cfg,'config_digest':image_manifest['config']['digest'],'config_source':'local content-addressed blob hash equals live registry manifest config descriptor','compressed_layer_bytes':sum(d['size'] for d in image_manifest['layers']),'config_bytes':len(cfg_raw),'manifest_bytes':len(raw),'consumer':mode}
        artifact_status,_,artifact_raw=http('/api/v2.0/projects/'+PROJECT+'/repositories/ani-modeldev-'+mode+'/artifacts/'+TAG)
        assert artifact_status==200
        artifact=json.loads(artifact_raw);assert artifact['digest']==digest
        entry['registry']['harbor_artifact']={k:artifact[k] for k in ('digest','size','type','manifest_media_type','media_type','push_time') if k in artifact}
        p=subprocess.run(['sudo','-n','ctr','-n','k8s.io','run','--rm',entry['image'],'ani-modeldev-d807-small-smoke-'+mode,('/ani-modeldev-step' if mode=='step' else '/usr/local/bin/ani-modeldev-webhook')],capture_output=True,timeout=60)
        entry['missing_configuration_startup']={'exit':p.returncode,'stdout_sha256':hashlib.sha256(p.stdout).hexdigest(),'stderr_sha256':hashlib.sha256(p.stderr).hexdigest(),'configuration_rejected':p.returncode!=0 and (b'stage=bootstrap class=configuration' in p.stderr+p.stdout if mode=='step' else b'modeldev webhook startup or serving failed' in p.stderr+p.stdout)}
        save()
    result['status']='PASS' if len(result['images'])==2 and all(x.get('registry') and x['missing_configuration_startup']['configuration_rejected'] for x in result['images'].values()) else 'BUILD_OR_PUBLICATION_FAILED'

if __name__=='__main__':
    try:main()
    except Exception as e:
        result['status']='FAILED';result['error_class']=type(e).__name__;result['error']=str(e)
    finally:
        if ROOT.exists():
            p=subprocess.run(['sudo','-n','systemctl','stop',UNIT],capture_output=True)
            result['cleanup']['daemon_stop_exit']=p.returncode
            if (ROOT/'small-auth').exists():shutil.rmtree(ROOT/'small-auth')
            result['cleanup']['auth_absent']=not (ROOT/'small-auth').exists()
            try:
                result['tasks_after']=ctr('tasks','ls','-q').decode().splitlines();result['original_tasks_missing']=sorted(set(result.get('tasks_before',[]))-set(result['tasks_after']))
                result['nodes_after']=health();result['disk_after']=dict(zip(('total','used','free'),shutil.disk_usage(ROOT)))
            except Exception as e:result['final_observation_error_class']=type(e).__name__
            save()
        print(json.dumps({'status':result['status'],'receipt':str(RECEIPT),'registry_push_issued':result['registry_push_issued']}))
