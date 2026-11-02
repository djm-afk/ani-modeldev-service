import subprocess,json,base64,ssl,socket,http.client,hashlib,datetime,urllib.parse

# Fixed original UID -> immutable full container ID edges already read from the
# live original Pod. Scheme stripping is an exact representation normalization,
# never a prefix match or a Pod-name-only fallback.
targets=[{'pod_name':'md-1cce950b-d6e9-453e-aa11-fffd8846224b-node-0-0-6d9lc','pod_uid':'49c82c2b-f819-4a03-b7b2-1789999cdd2e','container_id':'containerd://4cddb476920a21444eba72d93ba814e17fda84cae8714fda23cc42be3e7fb5ab'},{'pod_name':'md-4a1a92f7-4d94-439d-a8d7-1833f38c7dbc-node-0-0-9rnr8','pod_uid':'d8fb153a-5763-4ae7-8d44-63e3f996a830','container_id':'containerd://f76126a2196a82f34f390ae377728bfefed945de11b2c47735dfbfab1b6faf06'}]
namespace='ani-modeldev-tenant-a-v21'
def get(*args):
 r=subprocess.run(['kubectl',*args,'-o','json'],capture_output=True,timeout=15)
 if r.returncode:raise RuntimeError('existing reference unavailable')
 return json.loads(r.stdout)
creds=get('-n','ani-observability','get','secret','ani-opensearch-admin')['data'];cert=get('-n','ani-observability','get','secret','ani-opensearch-node-tls')['data'];service=get('-n','ani-observability','get','svc','ani-opensearch-master')
host='ani-opensearch-master.ani-observability.svc.cluster.local';auth='Basic '+base64.b64encode(base64.b64decode(creds['username'])+b':'+base64.b64decode(creds['password'])).decode();context=ssl.create_default_context(cadata=base64.b64decode(cert['ca.crt']).decode())
def request(method,path,body=None):
 c=http.client.HTTPSConnection(host,9200,context=context,timeout=8);c.sock=context.wrap_socket(socket.create_connection((service['spec']['clusterIP'],9200),timeout=8),server_hostname=host);encoded=json.dumps(body,separators=(',',':')).encode() if body is not None else None;c.request(method,path,body=encoded,headers={'Authorization':auth,'Accept':'application/json','Content-Type':'application/json'});r=c.getresponse();raw=r.read((1<<20)+1);status=r.status;c.close()
 if len(raw)>1<<20:raise RuntimeError('bounded metadata response exceeded')
 return status,raw
def value(source,path):
 for field in path.split('.'):
  if not isinstance(source,dict) or field not in source:return None
  source=source[field]
 return source
def exact(caps,options):
 return next((f for f in options if f in caps and any(t in caps[f] for t in ['keyword','constant_keyword'])),None)
indices=['ani-logs-2026.10.29','ani-logs-2026.10.30'];ipath=','.join(indices)
container_fields=['kubernetes.docker_id.keyword','kubernetes.docker_id','container.id.keyword','container.id','container_id.keyword','container_id','kubernetes.container_id.keyword','kubernetes.container_id']
namespace_fields=['kubernetes.namespace_name.keyword','kubernetes.namespace_name','k8s.namespace.name.keyword','k8s.namespace.name'];pod_fields=['kubernetes.pod_name.keyword','kubernetes.pod_name','k8s.pod.name.keyword','k8s.pod.name'];fields=container_fields+namespace_fields+pod_fields+['kubernetes.container_name.keyword','kubernetes.container_name','@timestamp','time']
out={'read_only':True,'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'indices':indices,'auth_source':'existing ani-opensearch-admin Secret; no credential output','tls_verified':True,'uid_container_binding_evidence':'open-live-metadata-log-hashes.json','query_limit':'one size=1 exact metadata/hash search per original container; no raw log output','queries':[]}
try:
 status,raw=request('GET','/'+ipath+'/_field_caps?fields='+urllib.parse.quote(','.join(fields),safe=',.'));out['field_caps']={'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
 if status!=200:out['result']='BLOCKED_FIELD_METADATA'
 else:
  caps=json.loads(raw).get('fields',{});out['field_caps']['fields']={f:sorted(t) for f,t in caps.items()};cid=exact(caps,container_fields);ns=exact(caps,namespace_fields);pod=exact(caps,pod_fields);out['selected_fields']={'container_id':cid,'namespace':ns,'pod_name':pod}
  if cid is None:out['result']='BLOCKED_NO_EXACT_CONTAINER_MAPPING'
  elif ns is None or pod is None:out['result']='BLOCKED_NO_EXACT_CROSSCHECK_MAPPING'
  else:
   for target in targets:
    ids=[target['container_id'],target['container_id'].split('://',1)[1]];query={'size':1,'track_total_hits':True,'timeout':'5s','query':{'bool':{'filter':[{'terms':{cid:ids}},{'term':{ns:namespace}},{'term':{pod:target['pod_name']}}]}},'_source':['@timestamp','time','kubernetes.docker_id','kubernetes.container_id','container.id','container_id','kubernetes.namespace_name','k8s.namespace.name','kubernetes.pod_name','k8s.pod.name','kubernetes.container_name','log','message']}
    if '@timestamp' in caps:query['sort']=[{'@timestamp':'asc'}]
    status,raw=request('POST','/'+ipath+'/_search',query);item={'original':target,'request':query,'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
    if status==200:
     data=json.loads(raw);item['total']=data.get('hits',{}).get('total');item['timed_out']=data.get('timed_out');item['shards']=data.get('_shards');item['hits']=[]
     for h in data.get('hits',{}).get('hits',[]):
      s=h.get('_source',{});observed_cid=value(s,cid.removesuffix('.keyword'));observed_ns=value(s,ns.removesuffix('.keyword'));observed_pod=value(s,pod.removesuffix('.keyword'));log=s.get('log',s.get('message',''));logbytes=log.encode() if isinstance(log,str) else json.dumps(log,sort_keys=True).encode()
      item['hits'].append({'index':h['_index'],'document_id':h['_id'],'timestamp':s.get('@timestamp',s.get('time')),'container_id':observed_cid,'namespace':observed_ns,'pod_name':observed_pod,'container_name':value(s,'kubernetes.container_name'),'original_uid_container_chain_matches':observed_cid in ids and observed_ns==namespace and observed_pod==target['pod_name'],'selected_source_sha256':hashlib.sha256(json.dumps(s,sort_keys=True,separators=(',',':')).encode()).hexdigest(),'log_bytes':len(logbytes),'log_sha256':hashlib.sha256(logbytes).hexdigest()})
     item['result']='FOUND_ORIGINAL_CONTAINER_LOG' if item['hits'] and all(h['original_uid_container_chain_matches'] for h in item['hits']) else 'NOTFOUND_EXACT_CONTAINER_LOG'
    else:item['result']='BLOCKED_CONTAINER_QUERY'
    out['queries'].append(item)
   out['result']='FOUND_ORIGINAL_CONTAINER_LOGS' if all(q['result']=='FOUND_ORIGINAL_CONTAINER_LOG' for q in out['queries']) else 'PARTIAL_OR_NOTFOUND'
except Exception as e:out.update(result='BLOCKED',exception_type=type(e).__name__)
print(json.dumps(out,indent=2))
