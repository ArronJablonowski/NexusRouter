package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func physicalRestartRecovery(t *testing.T, ctx context.Context, client *Client, local string, blocked <-chan struct{}, task Task, restart func()) {
	t.Helper()
	routes, err := OpenRouteStore(filepath.Join(local, "restart-routes"))
	if err != nil {
		t.Fatal(err)
	}
	task.Prompt = "block-request"
	running, err := client.DispatchRecorded(ctx, routes, "node-a", "physical-restart-running", task)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("crash fixture never started")
	}
	before, err := client.Status(ctx, "node-a", "physical-restart-running")
	if err != nil || before.State != "running" || len(before.TaskIDs) != 1 {
		t.Fatal("missing durable running task", before, err)
	}
	queuedTask := task
	queuedTask.Prompt = "Return a short answer."
	queued, err := client.DispatchRecorded(ctx, routes, "node-a", "physical-restart-queued", queuedTask)
	if err != nil || queued.State != "queued" {
		t.Fatal("fixture not queued", queued, err)
	}
	restart()
	until := time.Now().Add(55 * time.Second)
	recovered := false
	for time.Now().Before(until) {
		active, e1 := client.Status(ctx, "node-a", "physical-restart-running")
		waiting, e2 := client.Status(ctx, "node-a", "physical-restart-queued")
		if e1 == nil && e2 == nil && active.State == "failed" && waiting.State == "succeeded" {
			if active.ID != running.ID || waiting.ID != queued.ID || len(active.TaskIDs) != 1 || active.TaskIDs[0] != before.TaskIDs[0] || active.ErrorCode != "execution_failed" {
				t.Fatal("recovery replaced lineage", active, waiting)
			}
			if active.Result == nil || active.Result.Text != "" || waiting.Result == nil || waiting.Result.Text != "physical fixture result" {
				t.Fatal("recovery fabricated or lost result", active, waiting)
			}
			events, e := client.Events(ctx, "node-a", "physical-restart-running", before.TaskIDs[0], 0)
			if e != nil || events.State != "failed" || len(events.Events) == 0 {
				t.Fatal("recovery history unavailable", events, e)
			}
			reopened, e := OpenRouteStore(filepath.Join(local, "restart-routes"))
			if e != nil {
				t.Fatal(e)
			}
			retry, e := client.DispatchRecorded(ctx, reopened, "node-a", "physical-restart-running", task)
			if e != nil || retry.ID != running.ID || retry.State != "failed" {
				t.Fatal("crashed task retry reexecuted", retry, e)
			}
			recovered = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("host failed to reconcile crashed and queued tasks within lease-recovery window")
	}
	t.Log("physical SIGKILL/restart preserved task identity, failed interrupted execution without replay and completed saved queued work")
}

const twoHostRestart = `
import sys,json,pathlib,os,signal,time
p=json.load(sys.stdin);d=pathlib.Path(p['directory'])
assert d.parent==pathlib.Path('/tmp') and d.name.startswith('nexus-two-host-')
assert json.loads((d/'owner.json').read_text())['binary']==p['binary']
old=int((d/'host.pid').read_text());args=(pathlib.Path('/proc')/str(old)/'cmdline').read_bytes().split(b'\0')
assert p['binary'].encode() in args and str(d/'journal').encode() in args
children={}
def identity(pid):
 try:
  root=pathlib.Path('/proc')/str(pid);stat=(root/'stat').read_text().rsplit(')',1)[1].split()
  return (stat[0],int(stat[1]),stat[19])
 except (FileNotFoundError,ProcessLookupError):return None
if p.get('check_harness_children'):
 ancestors={old}
 for _ in range(16):
  changed=False
  for path in pathlib.Path('/proc').iterdir():
   if not path.name.isdigit():continue
   pid=int(path.name);value=identity(pid)
   if value and value[1] in ancestors and pid not in ancestors:
    ancestors.add(pid);children[pid]=value[2];changed=True
  if not changed:break
 assert children,'running harness had no owned child process'
(d/'restart.request').write_text('one owned fixture restart')
os.kill(old,signal.SIGKILL)
if children:
 deadline=time.monotonic()+5
 def surviving():
  return [pid for pid,start in children.items() if (v:=identity(pid)) and v[2]==start and v[0]!='Z']
 while surviving() and time.monotonic()<deadline:time.sleep(.05)
 survivors=surviving()
 if survivors:
  for pid in survivors:os.kill(pid,signal.SIGKILL)
  raise RuntimeError('owned harness child survived host crash; fixture children stopped')
until=time.monotonic()+10
while time.monotonic()<until:
 try:new=int((d/'host.pid').read_text())
 except ValueError:new=old
 if new!=old and pathlib.Path('/proc',str(new)).exists():
  print(json.dumps({'previous_pid':old,'new_pid':new,'exited_harness_children':len(children)}));sys.exit(0)
 time.sleep(.05)
raise RuntimeError('fixture restart failed')
`

func validPhysicalRestart(data []byte) bool {
	var result struct {
		Previous int `json:"previous_pid"`
		New      int `json:"new_pid"`
	}
	return json.Unmarshal(data, &result) == nil && result.Previous > 0 && result.New > 0 && result.Previous != result.New
}
