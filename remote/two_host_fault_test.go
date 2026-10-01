package remote

// Test-only TLS response-loss injector on the remote host. Both legs use the
// disposable fixture CA and credentials; it never loads operator credentials.
// It captures a complete successful destination receipt, fsyncs it, then closes
// the caller socket before returning any HTTP response for each lost-key POST.
const twoHostFaultProxy = `
import sys,ssl,socket,http.client,http.server,pathlib,hashlib,os,json,threading
root=pathlib.Path(sys.argv[1]);address=sys.argv[2];port=int(sys.argv[3]);backend=int(sys.argv[4])
server_context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
server_context.minimum_version=ssl.TLSVersion.TLSv1_3
server_context.load_cert_chain(root/'cert.pem',root/'key.pem')
server_context.load_verify_locations(root/'ca.pem');server_context.verify_mode=ssl.CERT_REQUIRED
client_context=ssl.create_default_context(cafile=str(root/'ca.pem'))
client_context.minimum_version=ssl.TLSVersion.TLSv1_3
client_context.load_cert_chain(root/'caller-cert.pem',root/'caller-key.pem')
lock=threading.Lock()
class Handler(http.server.BaseHTTPRequestHandler):
 protocol_version='HTTP/1.1'
 def log_message(self,*args):pass
 def do_GET(self):self.forward()
 def do_POST(self):self.forward()
 def forward(self):
  length=int(self.headers.get('Content-Length','0'))
  if length<0 or length>1048576:raise ValueError('invalid fixture body')
  body=self.rfile.read(length)
  connection=http.client.HTTPConnection(address,backend,timeout=20)
  raw=socket.create_connection((address,backend),timeout=8)
  connection.sock=client_context.wrap_socket(raw,server_hostname='node-a')
  try:
   connection.request(self.command,self.path,body=body,headers=dict(self.headers))
   response=connection.getresponse();data=response.read(1048577)
   if len(data)>1048576:raise ValueError('oversized fixture response')
   drop=False
   if self.command=='POST' and '-lost-' in self.path and not self.path.endswith('/cancel') and response.status<300:
    receipt=root/('receipt-'+hashlib.sha256(self.path.encode()).hexdigest()+'.json')
    with lock:
     if not receipt.exists():
      decoded=json.loads(data);assert decoded.get('id')
      fd=os.open(receipt,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
      with os.fdopen(fd,'wb') as out:out.write(data);out.flush();os.fsync(out.fileno())
      drop=True
   if drop:
    self.close_connection=True
    self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
   self.send_response(response.status)
   for name,value in response.getheaders():
    if name.lower() not in ['content-length','transfer-encoding','connection','server','date']:
     self.send_header(name,value)
   self.send_header('Content-Length',str(len(data)));self.send_header('Connection','close');self.end_headers()
   self.wfile.write(data);self.close_connection=True
  finally:connection.close()
server=http.server.ThreadingHTTPServer((address,port),Handler)
server.socket=server_context.wrap_socket(server.socket,server_side=True)
server.serve_forever()
`

const twoHostFaultReceipt = `
import sys,json,pathlib,hashlib
p=json.load(sys.stdin);d=pathlib.Path(p['directory'])
assert d.parent==pathlib.Path('/tmp') and d.name.startswith('nexus-two-host-')
assert json.loads((d/'owner.json').read_text())['binary']==p['binary']
path='/v1/remote/tasks/'+p['key']
print((d/('receipt-'+hashlib.sha256(path.encode()).hexdigest()+'.json')).read_text())
`
