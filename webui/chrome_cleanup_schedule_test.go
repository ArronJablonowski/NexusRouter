package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChromeCleanupScheduleControls(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	enabled, hour, minute, day := true, 3, 30, any(nil)
	generation, saves := 1, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/session/csrf" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":1,"csrf_token":"fixture"}`))
			return
		}
		if r.URL.Path == "/app/api/v1/cleanup-schedule" {
			mu.Lock()
			defer mu.Unlock()
			recovered := false
			if r.Method == "POST" {
				var input struct {
					Expected string `json:"expected_digest"`
					Settings struct {
						Enabled bool `json:"enabled"`
						Hour    int  `json:"hour"`
						Minute  int  `json:"minute"`
						Day     any  `json:"weekday"`
					} `json:"settings"`
				}
				if r.Header.Get("X-Darwin-CSRF") != "fixture" || json.NewDecoder(r.Body).Decode(&input) != nil {
					http.Error(w, "denied", 403)
					return
				}
				saves++
				if input.Settings.Hour == 7 || input.Expected != fmt.Sprintf("%064x", generation) {
					http.Error(w, "changed", 409)
					return
				}
				if input.Settings.Hour == 8 {
					http.Error(w, "unconfirmed", 503)
					return
				}
				if input.Settings.Hour == 9 {
					recovered = true
				} else {
					enabled, hour, minute, day = input.Settings.Enabled, input.Settings.Hour, input.Settings.Minute, input.Settings.Day
					generation++
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"version": 1, "recovered": recovered, "schedule": map[string]any{"enabled": enabled, "hour": hour, "minute": minute, "weekday": day, "loaded": enabled, "running": false, "state": "ready", "timezone": "MDT", "digest": fmt.Sprintf("%064x", generation)}})
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
await eventually('document.querySelector("#cleanup-schedule-status")?.textContent.includes("Automatic cleanup is on")','schedule missing');
await evaluate('document.querySelector("#cleanup-auto-enabled").checked=false;document.querySelector("#cleanup-schedule-save").click()');
await eventually('document.querySelector("#cleanup-schedule-status").textContent.includes("Automatic cleanup is off")','disable failed');
await evaluate('document.querySelector("#cleanup-frequency").value="weekly";document.querySelector("#cleanup-frequency").dispatchEvent(new Event("change"));document.querySelector("#cleanup-weekday").value="2";document.querySelector("#cleanup-time").value="04:15";document.querySelector("#cleanup-schedule-save").click()');
await eventually('!document.querySelector("#cleanup-schedule-save").disabled','weekly save did not finish');
await evaluate('document.querySelector("#cleanup-time").value=""');await cdp('Page.reload');await eventually('document.querySelector("#cleanup-time")?.value==="04:15"&&!document.querySelector("#cleanup-auto-enabled").disabled','reload failed');
if(!await evaluate('document.querySelector("#cleanup-frequency").value==="weekly"&&document.querySelector("#cleanup-weekday").value==="2"&&!document.querySelector("#cleanup-auto-enabled").checked'))throw Error('weekly state lost');
for(const width of [1440,390,320]){await cdp('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false});if(await evaluate('document.documentElement.scrollWidth>innerWidth'))throw Error('schedule overflow '+width);}
await evaluate('document.querySelector("#cleanup-auto-enabled").checked=true;document.querySelector("#cleanup-schedule-save").click()');
await eventually('document.querySelector("#cleanup-schedule-status").textContent.includes("Automatic cleanup is on")','enable failed');
for(const [hour,message] of [['07:15','changed elsewhere'],['08:15','could not be confirmed'],['09:15','previous schedule was recovered']]){
 await evaluate('document.querySelector("#cleanup-time").value='+JSON.stringify(hour)+';document.querySelector("#cleanup-schedule-save").click()');
 await eventually('document.querySelector("#cleanup-schedule-status").textContent.includes('+JSON.stringify(message)+')','missing '+message);
 if(hour!=='09:15'&&!await evaluate('document.querySelector("#cleanup-time").value==='+JSON.stringify(hour)))throw Error('draft discarded');
 await evaluate('document.querySelector("#cleanup-schedule-refresh").click()');await eventually('document.querySelector("#cleanup-time").value==="04:15"&&!document.querySelector("#cleanup-schedule-save").disabled','reload recovery failed');
}
socket.close();`
	if out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if saves != 6 {
		t.Fatal("unexpected replay", saves)
	}
}
