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

func TestChromeRemoteModelsAndStatusSeparation(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/advance":
			stage.Add(1)
		case "/app/api/v1/schedules":
			w.Write([]byte(`{"version":1,"observed_at":"2026-10-04T18:00:00Z","items":[{"id":"health","description":"Record provider health","enabled":true,"interval":"30s"}]}`))
		case "/app/api/v1/os-schedules":
			w.Write([]byte(`{"version":1,"observed_at":"2026-10-04T18:00:00Z","items":[{"id":"timer","name":"Example timer","source":"system timer","schedule":"Every 5m","state":"Configured"}],"limitations":[]}`))
		case "/app/api/v1/models":
			w.Write([]byte(chromeModelsPopulatedFixture))
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
		case "/app/api/v1/remote-membership":
			w.Write([]byte(`{"version":1,"enabled":true,"inspection_enabled":true,"registry":{"peers":[{"id":"spark","operations":["info"]},{"id":"denied","operations":[]}]}}`))
		case "/app/api/v1/remote-inspection":
			if r.Header.Get("X-Darwin-CSRF") != "fixture" {
				http.Error(w, "denied", 403)
				return
			}
			if stage.Load() > 1 {
				http.Error(w, "unavailable", 503)
				return
			}
			name := "spark-host"
			if stage.Load() == 1 {
				name = "spark-renamed"
			}
			writeChromeJSON(w, map[string]any{"version": 1, "observed_at": "2026-10-04T18:00:00Z", "info": map[string]any{"version": 1, "instance": "spark", "hostname": name, "available": true, "models": []any{map[string]any{"id": "worker", "model": "remote-worker", "provider": "ollama", "local": true, "context_tokens": 32768, "capabilities": []string{"coding"}, "observation": map[string]any{"state": "present", "checked_at": "2026-10-04T18:00:00Z"}}}}})
		default:
			shell.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/models'});
 await eventually('document.querySelector("#remote-model-hosts").textContent.includes("remote-worker")','remote inventory absent');
 if(!await evaluate('document.querySelector("#chat-view").hidden&&document.querySelector("#status-view").hidden&&!document.querySelector("#models-view").hidden&&document.querySelector("#remote-model-hosts").textContent.includes("not permitted")&&!document.querySelector("#model-list")'))throw Error('separation or permissions');
 await evaluate('window.row=document.querySelector("#remote-model-hosts li");window.detail=row.querySelector(".model-details");row.querySelector(".model-card-toggle").click();fetch("/fixture/advance")');
 await evaluate('document.querySelector("#refresh-models").click()');
 await eventually('document.querySelector("#remote-model-hosts").textContent.includes("spark-renamed")','hostname did not refresh');
 if(!await evaluate('row===document.querySelector("#remote-model-hosts li")&&!detail.hidden&&row.querySelector(".model-card-toggle").getAttribute("aria-expanded")==="true"'))throw Error('refresh rebuilt cards');
 await evaluate('fetch("/fixture/advance")');await evaluate('document.querySelector("#refresh-models").click()');
 await eventually('document.querySelector("#remote-model-hosts").textContent.includes("Inventory unavailable")','failure not explicit');
 if(!await evaluate('document.querySelectorAll("#remote-model-hosts li").length===0'))throw Error('stale availability');
 await cdp('Page.navigate',{url:origin+'/app/status'});
 await eventually('!document.querySelector("#status-view").hidden','status absent');
 if(!await evaluate('document.querySelector("#chat-view").hidden&&document.querySelector("#models-view").hidden&&document.querySelector("#status-view #inspector")!==null&&document.querySelector("[data-view=status]").getAttribute("aria-current")==="page"'))throw Error('status navigation');
 await cdp('Page.navigate',{url:origin+'/app/cron'});
 await eventually('document.querySelector("#cron-local-list").textContent.includes("Record provider health")&&document.querySelector("#cron-os-hosts").textContent.includes("Example timer")','cron records missing');
 if(!await evaluate('document.querySelector("#chat-view").hidden&&!document.querySelector("#cron-view").hidden&&document.querySelector("#status-view").hidden&&document.querySelector("#cron-view").textContent.includes("Local scheduled tasks")&&document.querySelector("#cron-view").textContent.includes("Remote scheduled tasks")&&document.querySelector(".os-schedules-panel").getBoundingClientRect().top>document.querySelector("#cron-remote-hosts").getBoundingClientRect().top'))throw Error('cron separation');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
