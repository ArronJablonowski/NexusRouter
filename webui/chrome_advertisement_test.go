package webui

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/remoteconfig"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChromeAdvertisementSettings(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(r *http.Request) bool { c, e := r.Cookie("darwin_session"); return e == nil && c.Value == "valid" }})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	writes := 0
	active := ToolAccessSettings{}
	saved := active
	digest := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/api/") {
			shell.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/app/api/v1/session/csrf" {
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture-token"})
			return
		}
		if r.URL.Path != "/app/api/v1/settings" {
			http.Error(w, "unavailable", 404)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			var input SettingsUpdateRequest
			if r.Header.Get("X-Darwin-CSRF") != "fixture-token" || decodeBoundedChromeJSON(w, r, 16384, &input) != nil || input.Validate() != nil {
				http.Error(w, "invalid", 400)
				return
			}
			if input.ExpectedDigest != digest {
				http.Error(w, "conflict", 409)
				return
			}
			saved = input.Settings
			writes++
			digest = strings.Repeat("b", 64)
		}
		writeChromeJSON(w, SettingsInspection{Version: 1, Digest: digest, Active: active, Saved: saved, RestartRequired: active != saved})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	preamble := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0]
	output, err := exec.CommandContext(ctx, node, "-e", preamble+chromeAdvertisementSteps, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("browser %v: %s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || saved.DNSLogging != "full" || saved.RemoteAdvertisement != (remoteconfig.Advertisement{Enabled: true, Interface: "en1", Name: "node-a", SSHPort: 2222}) {
		t.Fatal(writes, saved)
	}
}

const chromeAdvertisementSteps = `
await cdp('Page.navigate',{url:origin+'/app/settings'});
await eventually('document.readyState==="complete" && document.querySelector("#settings-status").textContent.includes("Saved settings") && !document.querySelector("#save-settings").disabled','settings unavailable');
if(await evaluate('document.querySelector("#remote-advertise-enabled").checked'))throw Error('enabled by default');
await evaluate('window.fixtureErrors=[];window.addEventListener("error",e=>fixtureErrors.push(e.message));document.querySelector("#remote-advertise-enabled").checked=true;document.querySelector("#tool-settings-form").dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}))');
await new Promise(r=>setTimeout(r,100));if(await evaluate('document.querySelector("#settings-validation").hidden'))throw Error(JSON.stringify(await evaluate('({errors:window.fixtureErrors,status:document.querySelector("#settings-status").textContent})')));
await evaluate('document.querySelector("#dns-logging").value="full";document.querySelector("#remote-advertise-interface").value="en1";document.querySelector("#remote-advertise-name").value="node-a";document.querySelector("#remote-advertise-ssh-port").value="2222";document.querySelector("#tool-settings-form").dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}))');
await eventually('!document.querySelector("#settings-restart-badge").hidden && !document.querySelector("#save-settings").disabled','saved restart state absent');
await evaluate('document.querySelector("#remote-advertise-name").value="changed";document.querySelector("#reset-settings").click()');
if(await evaluate('document.querySelector("#remote-advertise-name").value!=="node-a"'))throw Error('reset failed');
await cdp('Page.reload');
await eventually('!document.querySelector("#save-settings").disabled && document.querySelector("#remote-advertise-name").value==="node-a"','saved settings not restored');
await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
if(await evaluate('document.documentElement.scrollWidth>document.documentElement.clientWidth'))throw Error('narrow settings overflow');
if(await evaluate('document.querySelector("#dns-logging").value!=="full"'))throw Error('DNS mode lost on reload');
if(await evaluate('!document.querySelector("#dns-logging-status").textContent.includes("off")'))throw Error('saved mode falsely reported active');
socket.close();
`
