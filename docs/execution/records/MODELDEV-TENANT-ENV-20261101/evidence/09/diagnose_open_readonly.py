import base64, datetime, glob, hashlib, http.client, json, os, re, socket, ssl, subprocess, urllib.parse

NS='ani-modeldev-tenant-a-v21'
TARGETS=[('1cce950b-d6e9-453e-aa11-fffd8846224b','md-1cce950b-d6e9-453e-aa11-fffd8846224b-node-0-0-6d9lc','49c82c2b-f819-4a03-b7b2-1789999cdd2e','4cddb476920a21444eba72d93ba814e17fda84cae8714fda23cc42be3e7fb5ab','bbd4ad22-5b1a-448a-b71a-1069e18adc2f','edcf4a1d-cdf9-44da-9f28-20593d79469f'),('4a1a92f7-4d94-439d-a8d7-1833f38c7dbc','md-4a1a92f7-4d94-439d-a8d7-1833f38c7dbc-node-0-0-9rnr8','d8fb153a-5763-4ae7-8d44-63e3f996a830','f76126a2196a82f34f390ae377728bfefed945de11b2c47735dfbfab1b6faf06','2c372d09-70a8-4557-8b60-20f7ecd7d861','1e9521b7-fc75-41f2-a406-690a7c142b73'),('eca4d47b-5911-4f0f-8b45-89c1be775925',None,None,None,'5f04cda7-4c66-4f16-a552-053ebd4e5b6f','d2acb3df-3979-4d60-aed5-16db0ce440fb')]
def command(args):return subprocess.run(args,capture_output=True,timeout=20)
def kget(*args):
 r=command(['kubectl',*args,'-o','json'])
 if r.returncode:raise RuntimeError('Kubernetes metadata read failed')
 return json.loads(r.stdout)
def read_protected(path):
 r=command(['sudo','-n','cat',path])
 if r.returncode:raise RuntimeError('existing projected file unavailable')
 return r.stdout
def claims(token):
 parts=token.decode().split('.');data=json.loads(base64.urlsafe_b64decode(parts[1]+'='*((4-len(parts[1])%4)%4)))
 return {k:data[k] for k in ['sub','aud','exp'] if k in data}
def request(endpoint,ca,token,path):
 u=urllib.parse.urlsplit(endpoint);host=u.hostname;port=u.port or 443
 ip=host
 if '.svc' in host:
  fields=host.split('.');ip=kget('-n',fields[1],'get','svc',fields[0])['spec']['clusterIP']
 context=ssl.create_default_context(cadata=ca.decode())
 c=http.client.HTTPSConnection(host,port,context=context,timeout=7)
 c.sock=context.wrap_socket(socket.create_connection((ip,port),timeout=7),server_hostname=host)
 c.request('GET',u.path.rstrip('/')+path,headers={'Authorization':'Bearer '+token.decode().strip(),'Accept':'application/json'})
 response=c.getresponse();raw=response.read((1<<20)+1);status=response.status;c.close()
 if len(raw)>1<<20:raise RuntimeError('bounded response exceeded')
 return status,raw

context=command(['kubectl','config','current-context']).stdout.decode().strip()
api=kget('config','view','--minify')['clusters'][0]['cluster']['server'];ks=kget('get','ns','kube-system')['metadata']['uid']
private=kget('-n','ani-system','get','secret','ani-resource-private');config=base64.b64decode(private['data']['config.yaml']).decode()
cluster=re.search(r'^\s*cluster_id:\s*["\']?([a-f0-9-]{36})',config,re.M).group(1)
assert (context,api,ks,cluster)==('kubernetes-admin@ani-kcn-full','https://lb.kubesphere.local:6443','3eeb5379-8748-4bf8-8f6d-d7a48ce3bc24','f39bf6e2-f3ed-4191-ada3-caeebf86c220')
assert command(['hostname']).stdout.decode().strip()=='node1'
out={'read_only':True,'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'target':{'context':context,'apiserver':api,'kube_system_uid':ks,'cluster_id':cluster},'node_log_files':[],'kfp_runs':[],'actual_control_reads':[]}
for execution,pod,uid,container,run,wuid in TARGETS:
 if pod is None:continue
 path=f'/var/log/pods/{NS}_{pod}_{uid}/node'
 r=command(['sudo','-n','stat','--printf=%F %s',path+'/0.log'])
 entry={'execution_id':execution,'pod_uid':uid,'expected_node_log_path':path+'/0.log','stat_exit_code':r.returncode,'stat_metadata':r.stdout.decode() if r.returncode==0 else None}
 listing=command(['sudo','-n','find',path,'-maxdepth','1','-type','f','-printf','%f %s\n']);entry['directory_listing_exit_code']=listing.returncode;entry['file_metadata']=listing.stdout.decode().splitlines() if listing.returncode==0 else []
 r=command(['sudo','-n','crictl','inspect',container]);entry['original_container_inspect_exit_code']=r.returncode
 if r.returncode==0:
  data=json.loads(r.stdout);entry['original_container_state']={k:data.get('status',{}).get(k) for k in ['id','state','createdAt','startedAt','finishedAt','exitCode','reason','logPath']}
 out['node_log_files'].append(entry)

import yaml
cm=kget('-n','ani-system','get','cm','modeldev-trainimg-main-20261029b');cfg=yaml.safe_load(cm['data']['config.yaml'])
pod=kget('-n','ani-system','get','pods','-l','app=ani-modeldev')['items'][0];assert pod['spec']['nodeName']=='node1';puid=pod['metadata']['uid'];root=f'/var/lib/kubelet/pods/{puid}/volumes/kubernetes.io~projected/'
ktoken=read_protected(root+'kfp-token/token');out['existing_kfp_token_claims']=claims(ktoken)
volume=next(v['name'] for v in pod['spec']['volumes'] if v['name'].startswith('kube-api-access-'));atoken=read_protected(root+volume+'/token');aca=read_protected(root+volume+'/ca.crt');out['existing_api_token_claims']=claims(atoken)
for execution,name,uid,container,run,wuid in TARGETS:
 try:
  status,raw=request(cfg['runtime']['pipeline']['endpoint'],cm['data']['kfp-ca.crt'].encode(),ktoken,'/apis/v2beta1/runs/'+run)
  item={'execution_id':execution,'run_id':run,'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
  if status==200:
   d=json.loads(raw);item['run']={k:d.get(k) for k in ['run_id','experiment_id','display_name','state','created_at','scheduled_at','finished_at','service_account','storage_state']};item['runtime_parameters']=d.get('runtime_config',{}).get('parameters',{});item['pipeline_root']=d.get('runtime_config',{}).get('gcs_output_directory');item['pipeline_version_reference']=d.get('pipeline_version_reference');item['top_level_keys']=sorted(d);item['tasks']=[{k:t.get(k) for k in ['task_id','run_id','display_name','state','start_time','end_time','child_tasks'] if k in t}|{'error_sha256':hashlib.sha256(json.dumps(t.get('error',{}),sort_keys=True).encode()).hexdigest()} for t in d.get('run_details',{}).get('task_details',[])]
  out['kfp_runs'].append(item)
 except Exception as e:out['kfp_runs'].append({'execution_id':execution,'run_id':run,'exception_type':type(e).__name__})
 if name is None:continue
 for path in ['/api/v1/namespaces/'+NS,'/api/v1/namespaces/'+NS+'/pods/'+name,'/api/v1/namespaces/'+NS+'/pods/'+name+'/log?container=node&timestamps=true&tailLines=1001&limitBytes=65537']:
  status,raw=request(cfg['runtime']['kubernetes']['endpoint'],aca,atoken,path);item={'execution_id':execution,'path':path,'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
  if '/log?' in path:item['kubelet_error_body']=bool(re.fullmatch(rb'unable to retrieve container logs for containerd://[a-f0-9]+\s*',raw));item['timestamp_prefix']=bool(re.match(rb'^\d{4}-\d\d-\d\dT',raw))
  elif status==200:item['object_uid']=json.loads(raw)['metadata']['uid']
  out['actual_control_reads'].append(item)
pods=kget('-n',NS,'get','pods')['items'];out['remaining_original_workflow_pods']=[]
for p in pods:
 owners=p['metadata'].get('ownerReferences',[]);matching=[o for o in owners if o.get('uid') in [t[5] for t in TARGETS]]
 if matching:out['remaining_original_workflow_pods'].append({'name':p['metadata']['name'],'uid':p['metadata']['uid'],'phase':p['status'].get('phase'),'owners':matching,'finalizers':p['metadata'].get('finalizers',[]),'container_exits':[{'name':c['name'],'restartCount':c.get('restartCount'),'terminated':{k:v for k,v in c.get('state',{}).get('terminated',{}).items() if k!='message'}} for c in p['status'].get('containerStatuses',[])]})
out['central_log_services']=[{'namespace':s['metadata']['namespace'],'name':s['metadata']['name'],'ports':[p['port'] for p in s['spec']['ports']]} for s in kget('get','svc','-A')['items'] if any(x in s['metadata']['name'] for x in ['loki','logging','elasticsearch','opensearch','victoria-logs'])]
print(json.dumps(out,indent=2))
