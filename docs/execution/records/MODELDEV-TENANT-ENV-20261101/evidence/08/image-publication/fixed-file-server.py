#!/usr/bin/env python3
"""Short lived fixed-file transport: exact client, nonce path, SHA, Range; no directory serving."""
import hashlib,http.server,ipaddress,json,os,secrets,signal,stat,sys,threading,time
from pathlib import Path

ROOT=Path(sys.argv[1]).resolve()
config=json.loads((ROOT/'sources.json').read_text())
os.umask(0o077)
assert len(config['allowed_clients'])==1
assert 60<=config['ttl_seconds']<=1800
assert ipaddress.ip_address(config['bind_ip']).version==4
allowed=set(config['allowed_clients'])
nonce=secrets.token_urlsafe(32)
opened={}
for key,value in config['files'].items():
    assert key and not key.startswith('/') and '..' not in key.split('/')
    fd=os.open(value['source'],os.O_RDONLY|os.O_NOFOLLOW)
    info=os.fstat(fd);assert stat.S_ISREG(info.st_mode) and info.st_size==value['size']
    h=hashlib.sha256();offset=0
    while block:=os.pread(fd,1024*1024,offset):h.update(block);offset+=len(block)
    assert h.hexdigest()==value['sha256'],key
    opened[key]=(fd,value,info)
started=time.time()
requests=[]
lock=threading.Lock()

def public_receipt():
    return {'pid':os.getpid(),'bind_ip':config['bind_ip'],'port':server.server_address[1],'allowed_clients':sorted(allowed),'started_at_unix':started,'expires_at_unix':started+config['ttl_seconds'],'file_count':len(opened),'files':{key:{'size':v['size'],'sha256':v['sha256']} for key,(_,v,_) in opened.items()},'requests':list(requests),'nonce_emitted':False}

class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def do_HEAD(self):self.send_file(False)
    def do_GET(self):self.send_file(True)
    def refuse(self,status):
        self.send_response(status);self.send_header('Content-Length','0');self.send_header('Connection','close');self.end_headers();self.close_connection=True
    def send_file(self,body):
        if self.client_address[0] not in allowed:
            with lock:requests.append({'client':self.client_address[0],'status':403,'key':None})
            return self.refuse(403)
        if time.time()>started+config['ttl_seconds']:return self.refuse(410)
        prefix='/'+nonce+'/'
        if not self.path.startswith(prefix):return self.refuse(404)
        key=self.path[len(prefix):]
        if key not in opened:return self.refuse(404)
        fd,value,info=opened[key]
        current=os.fstat(fd)
        if (current.st_ino,current.st_size,current.st_mtime_ns)!=(info.st_ino,info.st_size,info.st_mtime_ns):return self.refuse(409)
        size=value['size'];start=0;end=size-1;status=200
        header=self.headers.get('Range')
        if header:
            import re
            match=re.fullmatch(r'bytes=(\d+)-(\d*)',header)
            if not match:return self.refuse(416)
            start=int(match[1]);end=int(match[2]) if match[2] else size-1
            if start<0 or start>=size or end<start or end>=size:return self.refuse(416)
            if self.headers.get('If-Range') not in (None,'"'+value['sha256']+'"'):return self.refuse(412)
            status=206
        self.send_response(status)
        self.send_header('Content-Type','application/octet-stream');self.send_header('Content-Length',str(end-start+1))
        self.send_header('Accept-Ranges','bytes');self.send_header('ETag','"'+value['sha256']+'"');self.send_header('X-Content-SHA256',value['sha256'])
        if status==206:self.send_header('Content-Range',f'bytes {start}-{end}/{size}')
        self.end_headers()
        sent=0
        try:
            if body:
                offset=start
                while offset<=end:
                    block=os.pread(fd,min(1024*1024,end-offset+1),offset)
                    if not block:raise RuntimeError('fixed source shortened')
                    self.wfile.write(block);sent+=len(block);offset+=len(block)
        except (BrokenPipeError,ConnectionResetError):pass
        finally:
            with lock:
                requests.append({'client':self.client_address[0],'status':status,'key':key,'range_start':start,'range_end':end,'bytes_written':sent,'head':not body})
                (ROOT/'public-receipt.json').write_text(json.dumps(public_receipt(),indent=2)+'\n')

class Server(http.server.ThreadingHTTPServer):
    daemon_threads=True
    allow_reuse_address=False
    def handle_error(self,request,client_address):
        with lock:requests.append({'client':client_address[0],'status':'HANDLER_ERROR','key':None})

server=Server((config['bind_ip'],0),Handler)
for sig in (signal.SIGTERM,signal.SIGINT):signal.signal(sig,lambda *_:threading.Thread(target=server.shutdown,daemon=True).start())
ready=public_receipt()
ready['base_url']='http://'+config['bind_ip']+':'+str(server.server_address[1])+'/'+nonce+'/'
ready['urls']={key:ready['base_url']+key for key in opened}
(ROOT/'ready-private.json').write_text(json.dumps(ready));(ROOT/'ready-private.json').chmod(0o600)
(ROOT/'public-receipt.json').write_text(json.dumps(public_receipt(),indent=2)+'\n')
timer=threading.Timer(config['ttl_seconds'],server.shutdown);timer.daemon=True;timer.start()
try:server.serve_forever(poll_interval=0.5)
finally:
    timer.cancel();server.server_close()
    for fd,_,_ in opened.values():os.close(fd)
    (ROOT/'ready-private.json').unlink(missing_ok=True)
    final=public_receipt();final['closed_at_unix']=time.time();final['private_nonce_file_absent']=not (ROOT/'ready-private.json').exists()
    (ROOT/'closed-public.json').write_text(json.dumps(final,indent=2)+'\n')
