import subprocess,json,base64,ssl,socket,http.client,hashlib,datetime,urllib.parse

def get(*args):
 r=subprocess.run(['kubectl',*args,'-o','json'],capture_output=True,timeout=15)
 if r.returncode:raise RuntimeError('existing reference unavailable')
 return json.loads(r.stdout)
creds=get('-n','ani-observability','get','secret','ani-opensearch-admin')['data'];cert=get('-n','ani-observability','get','secret','ani-opensearch-node-tls')['data'];service=get('-n','ani-observability','get','svc','ani-opensearch-master')
host='ani-opensearch-master.ani-observability.svc.cluster.local';auth='Basic '+base64.b64encode(base64.b64decode(creds['username'])+b':'+base64.b64decode(creds['password'])).decode();context=ssl.create_default_context(cadata=base64.b64decode(cert['ca.crt']).decode())
def request(method,path,body=None):
 c=http.client.HTTPSConnection(host,9200,context=context,timeout=8);c.sock=context.wrap_socket(socket.create_connection((service['spec']['clusterIP'],9200),timeout=8),server_hostname=host);encoded=json.dumps(body,separators=(',',':')).encode() if body is not None else None;c.request(method,path,body=encoded,headers={'Authorization':auth,'Accept':'application/json','Content-Type':'application/json'});r=c.getresponse();raw=r.read((1<<20)+1);status=r.status;c.close()
 if len(raw)>1<<20:raise RuntimeError('bounded metadata/query response exceeded')
 return status,raw
indices=['ani-logs-2026.10.29','ani-logs-2026.10.30'];index_path=','.join(indices)
out={'read_only':True,'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'indices':indices,'tls_verified':True,'auth_source':'existing ani-opensearch-admin Secret; no credential output','queries':[]}
fields=['kubernetes.pod_id','kubernetes.pod_id.keyword','kubernetes.pod_uid','kubernetes.pod_uid.keyword','kubernetes.pod.uid','kubernetes.pod.uid.keyword','k8s.pod.uid','k8s.pod.uid.keyword','kubernetes.namespace_name','kubernetes.namespace_name.keyword','k8s.namespace.name','k8s.namespace.name.keyword','@timestamp','time','metadata.uid','metadata.uid.keyword','workflow.uid','workflow.uid.keyword','log','message']
try:
 status,raw=request('GET','/'+index_path+'/_field_caps?fields='+urllib.parse.quote(','.join(fields),safe=',.'))
 out['field_caps']={'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
 if status!=200:out['result']='BLOCKED_FIELD_METADATA'
 else:
  caps=json.loads(raw).get('fields',{});out['field_caps']['fields']={name:sorted(types) for name,types in caps.items()}
  uid=next((f for f in ['kubernetes.pod_id.keyword','kubernetes.pod_uid.keyword','kubernetes.pod.uid.keyword','k8s.pod.uid.keyword','kubernetes.pod_id','kubernetes.pod_uid','kubernetes.pod.uid','k8s.pod.uid'] if f in caps and any(t in caps[f] for t in ['keyword','constant_keyword'])) ,None)
  namespace=next((f for f in ['kubernetes.namespace_name.keyword','k8s.namespace.name.keyword','kubernetes.namespace_name','k8s.namespace.name'] if f in caps and any(t in caps[f] for t in ['keyword','constant_keyword'])),None)
  stamp=next((f for f in ['@timestamp','time'] if f in caps and 'date' in caps[f]),None)
  out['selected_fields']={'pod_uid':uid,'namespace':namespace,'timestamp':stamp}
  if uid is None or namespace is None:out['result']='BLOCKED_NO_EXACT_UID_MAPPING'
  else:
   uids=['49c82c2b-f819-4a03-b7b2-1789999cdd2e','d8fb153a-5763-4ae7-8d44-63e3f996a830'];filters=[{'terms':{uid:uids}},{'term':{namespace:'ani-modeldev-tenant-a-v21'}}]
   query={'size':1000,'track_total_hits':True,'timeout':'5s','query':{'bool':{'filter':filters}},'_source':['@timestamp','time','kubernetes','k8s','log','message']}
   if stamp:query['sort']=[{stamp:'asc'}]
   status,raw=request('POST','/'+index_path+'/_search',query);item={'request':query,'http_status':status,'response_bytes':len(raw),'response_sha256':hashlib.sha256(raw).hexdigest()}
   if status==200:
    d=json.loads(raw);hits=d.get('hits',{});item['total']=hits.get('total');item['timed_out']=d.get('timed_out');item['shards']=d.get('_shards');item['documents']=[]
    for h in hits.get('hits',[]):
     s=h.get('_source',{});k=s.get('kubernetes',{});text=s.get('log',s.get('message',''));rawtext=text.encode() if isinstance(text,str) else json.dumps(text,sort_keys=True).encode();row={'index':h['_index'],'document_id':h['_id'],'timestamp':s.get('@timestamp',s.get('time')),'pod_uid':k.get('pod_id',k.get('pod_uid',k.get('pod',{}).get('uid'))),'pod_name':k.get('pod_name'),'namespace':k.get('namespace_name'),'container':k.get('container_name'),'log_bytes':len(rawtext),'log_sha256':hashlib.sha256(rawtext).hexdigest(),'source_sha256':hashlib.sha256(json.dumps(s,sort_keys=True,separators=(',',':')).encode()).hexdigest()};item['documents'].append(row)
    out['result']='FOUND_ORIGINAL_UID_LOGS' if item['documents'] else 'NOTFOUND_ORIGINAL_UID_LOGS'
   else:out['result']='BLOCKED_UID_QUERY'
   out['queries'].append(item)
  out['workflow_archive_probe']={'result':'NOTFOUND_ARCHIVE_INDEX_IN_OBSERVED_METADATA','scope':'index metadata has ani-logs/date and internal top_queries/security indices; no dedicated workflow/argo archive index observed','exact_uid_field_available':any(k in caps for k in ['metadata.uid','metadata.uid.keyword','workflow.uid','workflow.uid.keyword'])}
except Exception as e:out.update(result='BLOCKED',exception_type=type(e).__name__)
print(json.dumps(out,indent=2))
