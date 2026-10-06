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

func TestChromeCronRetainsTransientFailureAndClearsRevocation(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var stage atomic.Int32
	schedules := map[string]any{"version": 1, "observed_at": "2026-10-05T18:00:00Z", "items": []any{map[string]any{"id": "worker", "description": "Scheduled worker", "interval": "30s", "enabled": true, "ai_usage": "possible"}}}
	osSchedules := map[string]any{"version": 1, "observed_at": "2026-10-05T18:00:00Z", "items": []any{map[string]any{"id": "timer", "name": "Example timer", "source": "system timer", "schedule": "Every 5m", "state": "Configured"}}, "limitations": []any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/advance":
			stage.Add(1)
		case "/app/api/v1/schedules":
			writeChromeJSON(w, schedules)
		case "/app/api/v1/os-schedules":
			writeChromeJSON(w, osSchedules)
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
		case "/app/api/v1/remote-membership":
			ops := []string{"info", "inspect"}
			if stage.Load() > 1 {
				ops = []string{}
			}
			writeChromeJSON(w, map[string]any{"version": 1, "enabled": true, "inspection_enabled": true, "registry": map[string]any{"peers": []any{map[string]any{"id": "spark", "operations": ops}}}})
		case "/app/api/v1/remote-inspection":
			if stage.Load() > 0 {
				http.Error(w, "unavailable", 503)
				return
			}
			var req struct {
				View string `json:"view"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid fixture request")
				return
			}
			if req.View == "os_schedules" {
				writeChromeJSON(w, map[string]any{"version": 1, "os_schedules": osSchedules})
				return
			}
			writeChromeJSON(w, map[string]any{"version": 1, "observed_at": "2026-10-05T18:00:00Z", "info": map[string]any{"instance": "spark", "hostname": "spark-host", "schedules": schedules}})
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
 await cdp('Page.navigate',{url:origin+'/app/cron'});
 await eventually('document.querySelectorAll("#cron-remote-hosts .cron-card").length===1&&document.querySelectorAll("#cron-os-hosts .cron-card").length===2','remote schedules missing');
 if(!await evaluate('document.querySelector("#cron-remote-hosts .cron-usage").textContent.includes("AI-capable")&&document.querySelector("#cron-os-hosts .cron-usage").textContent.includes("unknown")'))throw Error('AI usage not distinguished');
 await evaluate('window.saved=document.querySelector("#cron-remote-hosts .cron-card");window.savedOS=document.querySelectorAll("#cron-os-hosts .cron-card")[1];fetch("/fixture/advance")');
 await evaluate('document.querySelector("#refresh-cron").click()');
 await eventually('document.querySelector("#cron-remote-state").textContent.includes("unavailable")','failure missing');
 if(!await evaluate('saved===document.querySelector("#cron-remote-hosts .cron-card")&&savedOS===document.querySelectorAll("#cron-os-hosts .cron-card")[1]&&document.querySelector("#cron-remote-hosts").textContent.includes("stale")'))throw Error('transient failure erased schedules');
 await evaluate('fetch("/fixture/advance")');await evaluate('document.querySelector("#refresh-cron").click()');
 await eventually('document.querySelector("#cron-remote-hosts").textContent.includes("not permitted")','revocation missing');
 if(!await evaluate('document.querySelectorAll("#cron-remote-hosts .cron-card").length===0&&document.querySelectorAll("#cron-os-hosts .cron-card").length===1'))throw Error('revoked schedules retained');
 socket.close();`
	out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}
