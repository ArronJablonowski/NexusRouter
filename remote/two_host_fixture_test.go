package remote

// These scripts run only through explicitly configured, strict-key SSH. They
// operate on an owned mkdtemp directory and the hash-verified supplied binary.
const twoHostPrepare = `
import sys,json,os,tempfile,pathlib,base64,hashlib,socket
p=json.load(sys.stdin)
assert hashlib.sha256(pathlib.Path(p['binary']).read_bytes()).hexdigest()==p['sha256']
assert set(p['files'])=={'cert.pem','key.pem','ca.pem','config.yaml','trust.json','caller-cert.pem','caller-key.pem','proxy.py'}
d=pathlib.Path(tempfile.mkdtemp(prefix='nexus-two-host-',dir='/tmp'))
def port(host):
 s=socket.socket();s.bind((host,0));v=s.getsockname()[1];s.close();return v
provider=port('127.0.0.1');listener=port(p['address'])
for name,encoded in p['files'].items():
 b=base64.b64decode(encoded,validate=True)
 if name=='config.yaml':b=b.replace(b'FIXTURE_DIRECTORY',str(d).encode()).replace(b'FIXTURE_PORT',str(provider).encode())
 f=os.open(d/name,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(f,'wb') as out:out.write(b)
(d/'owner.json').write_text(json.dumps({'binary':p['binary']}))
print(json.dumps({'directory':str(d),'port':listener,'proxy_port':port(p['address']),'provider_port':provider}))
`
const twoHostRun = `
import sys,json,pathlib,subprocess,os
p=json.load(sys.stdin);d=pathlib.Path(p['directory']);assert str(d).startswith('/tmp/nexus-two-host-')
assert json.loads((d/'owner.json').read_text())['binary']==p['binary']
env=dict(os.environ);env['DARWIN_PROCESS_OWNER_DIR']=str(d/'owners')
if p.get('fixture_path'):
 assert all(pathlib.Path(v).is_absolute() for v in p['fixture_path'].split(':'))
 env['PATH']=p['fixture_path']
args=[p['binary'],'remote','serve','--instance','node-a','--listen',p['address']+':'+str(p['port']),'--config',str(d/'config.yaml'),'--journal',str(d/'journal'),'--trust',str(d/'trust.json'),'--cert',str(d/'cert.pem'),'--key',str(d/'key.pem'),'--ca',str(d/'ca.pem')]
if p.get('advertise_interface'):args+=['--advertise-interface',p['advertise_interface'],'--advertise-name','node-a','--advertise-ssh-port','22']
with (d/'host.log').open('w') as log:
 proxy=subprocess.Popen([sys.executable,str(d/'proxy.py'),str(d),p['address'],str(p['proxy_port']),str(p['port'])],stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
 (d/'proxy.pid').write_text(str(proxy.pid))
 while True:
  child=subprocess.Popen(args,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT,env=env,start_new_session=True)
  (d/'host.pid').write_text(str(child.pid))
  code=child.wait()
  restart=d/'restart.request'
  if not restart.exists():sys.exit(code)
  restart.unlink()

`
const twoHostRevoke = `
import sys,json,pathlib,subprocess
p=json.load(sys.stdin);d=pathlib.Path(p['directory']);assert str(d).startswith('/tmp/nexus-two-host-')
assert json.loads((d/'owner.json').read_text())['binary']==p['binary']
r=subprocess.run([p['binary'],'remote','revoke','--trust',str(d/'trust.json'),'--instance','node-b','--expected',p['digest']],capture_output=True,text=True,timeout=10)
print(r.stdout);print(r.stderr,file=sys.stderr);sys.exit(r.returncode)
`
const twoHostCleanup = `
import sys,json,pathlib,os,signal,time,shutil
p=json.load(sys.stdin);d=pathlib.Path(p['directory']);assert str(d).startswith('/tmp/nexus-two-host-') and d.parent==pathlib.Path('/tmp')
assert d.stat().st_uid==os.getuid() and json.loads((d/'owner.json').read_text())['binary']==p['binary']
for filename,marker in [('host.pid',str(d/'journal')),('proxy.pid',str(d/'proxy.py'))]:
 f=d/filename
 if not f.exists():continue
 pid=int(f.read_text());proc=pathlib.Path('/proc')/str(pid)
 def owned():
  try:args=(proc/'cmdline').read_bytes().split(b'\0');return marker.encode() in args and (filename=='proxy.pid' or p['binary'].encode() in args)
  except FileNotFoundError:return False
 if owned():
  os.kill(pid,signal.SIGTERM)
  until=time.monotonic()+5
  while owned() and time.monotonic()<until:time.sleep(.05)
  if owned():os.kill(pid,signal.SIGKILL)
  until=time.monotonic()+3
  while owned() and time.monotonic()<until:time.sleep(.05)
  assert not owned(),'fixture still alive'
if (d/'host.log').exists():print((d/'host.log').read_text()[-4096:])
shutil.rmtree(d)
print('owned fixture directory removed')
`
