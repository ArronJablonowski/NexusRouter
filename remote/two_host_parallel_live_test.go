package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"os"
	"strings"
	"testing"
	"time"
)

// Uses the explicitly owned provider from TestPhysicalTwoHostLiveModel. Real
// residency plus overlapping durable reservations distinguishes parallel model
// execution from merely dispatching two requests into a serial queue.
func qualifyParallelModels(t *testing.T, ctx context.Context, client *Client, admin func(context.Context, string, []byte) ([]byte, error), directory, endpoint, firstModel, secondModel string) {
	t.Helper()
	routeDir := t.TempDir()
	if err := os.Chmod(routeDir, 0700); err != nil {
		t.Fatal(err)
	}
	routes, err := OpenRouteStore(routeDir)
	if err != nil {
		t.Fatal(err)
	}
	first := testTask()
	first.ContextTokens = 8192
	first.Prompt = "List the integers from 1 through 300, in order, separated by commas. Do not skip any numbers. No commentary. /no_think"
	second := first
	second.ModelID = "second"
	second.Prompt = "Reply with only the integer that equals 17 times 23. /no_think"
	if _, err := client.DispatchRecorded(ctx, routes, "node-a", "parallel-first-task", first); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DispatchRecorded(ctx, routes, "node-a", "parallel-second-task", second); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"directory": directory, "endpoint": endpoint})
	script := `import json,sys,pathlib,sqlite3,urllib.request
p=json.load(sys.stdin);d=pathlib.Path(p['directory']);assert str(d).startswith('/tmp/nexus-two-host-')
m={k:int(v.split()[0])*1024 for k,v in (x.split(':',1) for x in pathlib.Path('/proc/meminfo').read_text().splitlines())}
c=sqlite3.connect('file:'+str(d/'owners/host-resources.db')+'?mode=ro',uri=True)
a=c.execute("select count(*) from reservations where state='active'").fetchone()[0]
r=json.load(urllib.request.urlopen(p['endpoint']+'/api/ps',timeout=2))
print(json.dumps({'available':m['MemAvailable'],'active':a,'residents':[x['name'] for x in r['models']]}))`
	overlap, residency, samples := false, false, 0
	minimum := ^uint64(0)
	complete := map[string]bool{}
	for until := time.Now().Add(3 * time.Minute); time.Now().Before(until); {
		body, e := admin(ctx, script, input)
		if e != nil {
			t.Fatalf("resource audit: %v %s", e, body)
		}
		var sample struct {
			Available uint64
			Active    int
			Residents []string
		}
		if e := json.Unmarshal(body, &sample); e != nil {
			t.Fatal(e)
		}
		samples++
		minimum = min(minimum, sample.Available)
		if sample.Available < resources.SparkRAMReserveBytes {
			t.Fatal("Spark reserve breached", sample.Available)
		}
		overlap = overlap || sample.Active >= 2
		models := strings.Join(sample.Residents, " ")
		residency = residency || (sample.Active >= 2 && strings.Contains(models, firstModel) && strings.Contains(models, secondModel))
		for index, id := range []string{"parallel-first-task", "parallel-second-task"} {
			if complete[id] {
				continue
			}
			result, e := client.Status(ctx, "node-a", id)
			if e != nil {
				t.Fatal(e)
			}
			if result.State == "failed" || result.State == "canceled" {
				t.Fatalf("parallel task: %+v", result)
			}
			if result.State != "succeeded" {
				continue
			}
			if result.Result == nil || len(result.TaskIDs) != 1 {
				t.Fatal("missing result", result)
			}
			if index == 1 && strings.TrimSpace(result.Result.Text) != "391" {
				t.Fatal("arithmetic mismatch", result.Result.Text)
			}
			if index == 0 && !strings.Contains(result.Result.Text, "300") {
				t.Fatal("incomplete sequence", result.Result.Text)
			}
			events, e := client.Events(ctx, "node-a", id, result.TaskIDs[0], 0)
			if e != nil {
				t.Fatal(e)
			}
			starts := 0
			want := firstModel
			if index == 1 {
				want = secondModel
			}
			for _, event := range events.Events {
				if event.Kind == runtime.TaskStarted {
					starts++
					if event.Data.ModelID != want || event.Data.ProviderID != "local" {
						t.Fatal("wrong actual model", event.Data)
					}
				}
			}
			if starts != 1 {
				t.Fatal("unexpected starts", starts)
			}
			complete[id] = true
			t.Logf("model=%s task=%s succeeded", want, result.TaskIDs[0])
		}
		if len(complete) == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(complete) != 2 || !overlap || !residency {
		t.Fatal(fmt.Sprintf("parallel evidence incomplete: completed=%d overlap=%v two_residents=%v", len(complete), overlap, residency))
	}
	t.Logf("parallel model reservations overlapped; both models resident; samples=%d minimum_available_bytes=%d reserve_bytes=%d", samples, minimum, resources.SparkRAMReserveBytes)
}
