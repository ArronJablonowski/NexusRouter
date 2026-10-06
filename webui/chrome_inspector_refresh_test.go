package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeInspectorUpdatesValuesInPlace(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var stage atomic.Int32
	var taskReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/api/v1/tasks/slow/route":
			taskReads.Add(1)
			time.Sleep(200 * time.Millisecond)
			writeChromeJSON(w, map[string]any{"version": 1, "task_id": "slow", "availability": "unavailable", "candidates": []any{}})
			return
		case "/fixture/advance":
			stage.Add(1)
			return
		case "/app/api/v1/health":
			if stage.Load() > 0 {
				time.Sleep(200 * time.Millisecond)
			}
			if stage.Load() > 1 {
				http.Error(w, "unavailable", 503)
				return
			}
			status := "healthy"
			if stage.Load() == 1 {
				status = "degraded"
			}
			checks := []map[string]any{}
			for i := 0; i < 30; i++ {
				checks = append(checks, map[string]any{"component": "provider", "id": "fixture-" + strconv.Itoa(i), "status": status, "code": "available"})
			}
			writeChromeJSON(w, map[string]any{"version": 1, "availability": "available", "status": status, "ready": true, "checked_at": "2026-10-04T12:00:00Z", "checks": checks})
			return
		case "/app/api/v1/resources":
			writeChromeJSON(w, map[string]any{"version": 1, "availability": "available", "observed_at": "2026-10-04T12:00:00Z", "cpus": 8})
			return
		case "/app/api/v1/models":
			writeChromeJSON(w, map[string]any{"version": 1, "availability": "unavailable", "models": []any{}})
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:origin+'/app/status'});
 await eventually('document.querySelectorAll("#health-details dd").length===33','health rows absent');
 await evaluate('window.rows=Array.from(document.querySelector("#health-details").children);window.valueNode=rows[1].firstChild;window.changes=0;window.observer=new MutationObserver(records=>{changes+=records.filter(r=>r.type==="childList").length});observer.observe(document.querySelector("#health-details"),{childList:true,subtree:true});document.querySelector("#inspector").scrollTop=200;window.savedScroll=document.querySelector("#inspector").scrollTop');
 await evaluate('fetch("/fixture/advance")');
 await evaluate('void(window.pending=window.NexusInspector.loadGlobals())');
 if(!await evaluate('rows.every((n,i)=>n===document.querySelector("#health-details").children[i])&&document.querySelector("#health-state").hidden'))throw Error('refresh blanked rows or showed loading');
 await evaluate('window.pending');
 if(!await evaluate('rows[1].textContent==="degraded"&&rows[1].firstChild===valueNode&&changes===0&&rows.every((n,i)=>n===document.querySelector("#health-details").children[i])&&document.querySelector("#inspector").scrollTop===savedScroll'))throw Error('refresh rebuilt nodes or moved scroll');
 await evaluate('window.changes=0;window.NexusInspector.loadGlobals()');
 if(!await evaluate('changes===0'))throw Error('unchanged response mutated row structure');
 await evaluate('fetch("/fixture/advance")');
 await evaluate('window.NexusInspector.loadGlobals()');
 if(!await evaluate('rows.every((n,i)=>n===document.querySelector("#health-details").children[i])&&rows[1].textContent==="degraded"'))throw Error('failure erased last values');
 await evaluate('window.firstTask=window.NexusInspector.loadTask("slow");window.secondTask=window.NexusInspector.loadTask("slow");window.samePending=firstTask===secondTask');
 if(!await evaluate('samePending'))throw Error('overlapping task refresh not coalesced');
 await evaluate('window.firstTask');
 if(!await evaluate('document.querySelector("#route-state").textContent==="Route unavailable."'))throw Error('slow task response discarded');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
	if taskReads.Load() != 1 {
		t.Fatalf("overlapping refresh made %d route reads", taskReads.Load())
	}
}
