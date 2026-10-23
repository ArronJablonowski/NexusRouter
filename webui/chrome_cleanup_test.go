package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeCleanupControls(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/session/csrf" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":1,"csrf_token":"fixture"}`))
			return
		}
		if r.URL.Path == "/app/api/v1/cleanup" {
			var input struct {
				Action string `json:"action"`
				Token  string `json:"token"`
			}
			if r.Method == "POST" {
				json.NewDecoder(r.Body).Decode(&input)
				if r.Header.Get("X-Darwin-CSRF") != "fixture" {
					http.Error(w, "csrf", 403)
					return
				}
			}
			out := map[string]any{"version": 1, "last_run": map[string]any{"time": "2026-10-16T00:00:00Z", "deleted": []string{}, "errors": 0, "complete": true}, "preview": nil, "result": nil}
			if input.Action == "preview" {
				out["preview"] = map[string]any{"token": strings.Repeat("a", 64), "items": []map[string]string{{"path": "resources/qa/" + strings.Repeat("long", 80), "kind": "qa"}}, "retained": []string{"backup1", "backup2", "backup3"}, "skipped": 1}
			}
			if input.Action == "run" {
				if input.Token != strings.Repeat("a", 64) {
					http.Error(w, "bad token", 400)
					return
				}
				n := runs.Add(1)
				if n == 3 {
					http.Error(w, "lost outcome", 503)
					return
				}
				if n == 1 {
					http.Error(w, "changed", 409)
					return
				}
				out["result"] = map[string]any{"time": "2026-10-16T00:00:00Z", "deleted": []string{"resources/qa/nexus-test"}, "errors": 0, "complete": true}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}
		if strings.Contains(r.URL.Path, "/api/") {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
await cdp('Page.navigate',{url:origin+'/app/settings'});
await eventually('document.querySelector("#cleanup-last")?.textContent.includes("Last run:")','last run missing');
if(!await evaluate('document.querySelector("#cleanup-run").disabled'))throw Error('run enabled before preview');
await evaluate('document.querySelector("#cleanup-preview").click()');
await eventually('!document.querySelector("#cleanup-run").disabled','preview did not enable run');
for(const width of [1440,390,320]){
 await cdp('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false});
 if(await evaluate('document.documentElement.scrollWidth>innerWidth'))throw Error('cleanup overflow '+width);
 if(!await evaluate('(()=>{const s=getComputedStyle(document.querySelector(".cleanup-card .settings-actions"));return s.display==="flex"&&parseFloat(s.gap)>=8&&s.flexWrap==="wrap"})()'))throw Error('cleanup button spacing '+width);
}
await evaluate('document.querySelector("#cleanup-run").click()');
await eventually('document.querySelector("#cleanup-status").textContent.includes("changed")','conflict missing');
if(!await evaluate('document.querySelector("#cleanup-run").disabled'))throw Error('stale run enabled');
await evaluate('document.querySelector("#cleanup-preview").click()');await eventually('!document.querySelector("#cleanup-run").disabled','repreview failed');
await evaluate('document.querySelector("#cleanup-run").click()');await eventually('document.querySelector("#cleanup-status").textContent.includes("Cleanup finished: 1")','run failed');
await evaluate('document.querySelector("#cleanup-preview").click()');await eventually('!document.querySelector("#cleanup-run").disabled','third preview failed');
await evaluate('document.querySelector("#cleanup-run").click()');await eventually('document.querySelector("#cleanup-status").textContent.includes("outcome unavailable")','uncertain outcome missing');
if(!await evaluate('document.querySelector("#cleanup-run").disabled'))throw Error('uncertain run enabled');
await cdp('Page.reload');await eventually('document.querySelector("#cleanup-last")?.textContent.includes("Last run:")','reload missing');
if(!await evaluate('document.querySelector("#cleanup-run").disabled'))throw Error('reload replay eligible');
socket.close();`
	if out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if runs.Load() != 3 {
		t.Fatal("run replay", runs.Load())
	}
}
