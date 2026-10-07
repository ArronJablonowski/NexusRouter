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

func TestChromeRoutingRemotePaths(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(r *http.Request) bool { c, e := r.Cookie("darwin_session"); return e == nil && c.Value == "valid" }})
	if err != nil {
		t.Fatal(err)
	}
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/empty" {
			stage.Store(1)
			return
		}
		if r.URL.Path == "/fixture/fail" {
			stage.Store(2)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/app/api/") {
			shell.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/app/api/v1/models":
			writeChromeJSON(w, map[string]any{"version": 1, "availability": "available", "models": []any{}, "fitness": []any{}, "rankings": []any{}, "local_concurrency": "auto", "local_pressure_policy": "wait", "local_ram_limit_pct": 80, "local_vram_limit_pct": 85, "managed_residency": false, "specialists_allow_cloud": false})
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture-token"})
		case "/app/api/v1/remote-membership":
			if stage.Load() == 2 {
				http.Error(w, "private error", 503)
				return
			}
			peers := []any{}
			if stage.Load() == 0 {
				peers = []any{map[string]any{"id": "spark", "transport": "ssh", "endpoint": "https://10.77.7.202:9443", "operations": []string{"info", "dispatch"}}, map[string]any{"id": "mini", "transport": "https", "endpoint": "https://10.77.7.222:9443", "operations": []string{"info"}}}
			}
			writeChromeJSON(w, map[string]any{"version": 1, "enabled": true, "inspection_enabled": true, "registry": map[string]any{"version": 1, "peers": peers}})
		case "/app/api/v1/remote-inspection":
			var input struct {
				Version        int
				Instance, View string
			}
			if r.Method != "POST" || r.Header.Get("X-Darwin-CSRF") != "fixture-token" || decodeBoundedChromeJSON(w, r, 4096, &input) != nil || input.View != "info" {
				http.Error(w, "invalid", 400)
				return
			}
			if input.Instance == "mini" {
				http.Error(w, "private error", 503)
				return
			}
			writeChromeJSON(w, map[string]any{"version": 1, "observed_at": time.Now().UTC(), "info": map[string]any{"version": 1, "instance": "spark", "available": true, "models": []any{map[string]any{"id": "coder", "provider": "ollama", "model": "Qwen3 Coder <img src=x onerror=alert(1)>", "local": true, "capabilities": []string{"coding"}, "context_tokens": 131072}}}})
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	preamble := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0]
	out, err := exec.CommandContext(ctx, node, "-e", preamble+chromeRoutingRemoteSteps, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("remote routing browser: %v %s", err, out)
	}
}

const chromeRoutingRemoteSteps = `
await cdp('Page.navigate',{url:origin+'/app/routing-map'});
await eventually('document.querySelector("#remote-route-status").textContent.includes("2 paired systems · 1 connected")','remote paths unavailable');
const result=await evaluate('({cards:document.querySelectorAll(".remote-route-card").length,specialists:document.querySelectorAll("#specialist-grid .specialist-card").length,text:document.querySelector("#remote-route-grid").textContent,below:document.querySelector(".remote-route-section").getBoundingClientRect().top>=document.querySelector("#specialist-grid").getBoundingClientRect().bottom,injected:document.querySelectorAll("#remote-route-grid img").length,branches:document.querySelectorAll("#routing-branches .routing-terminal").length})');
if(!result.text.includes('Host IP: 10.77.7.202')||!result.text.includes('Host IP: 10.77.7.222'))throw Error('remote host IP missing');
if(result.cards!==2||result.specialists!==14||!result.below||result.injected||result.branches!==16||!result.text.includes('Connection unavailable'))throw Error(JSON.stringify(result));
const sizing=await evaluate('({remote:document.querySelector(".remote-route-card").getBoundingClientRect().width,local:document.querySelector("#specialist-grid .specialist-card").getBoundingClientRect().width,remoteHeight:document.querySelector(".remote-route-card").getBoundingClientRect().height,localHeight:Math.max(...[...document.querySelectorAll("#specialist-grid .specialist-card")].map(c=>c.getBoundingClientRect().height)),color:getComputedStyle(document.querySelector(".remote-route-card")).borderTopColor,animation:getComputedStyle(document.querySelector(".remote-route-card .core-rings span")).animationName})');
if(Math.abs(sizing.remote-sizing.local)>1||Math.abs(sizing.remoteHeight-sizing.localHeight)>1||sizing.color!=='rgb(255, 229, 106)'||sizing.animation!=='core-pulse')throw Error(JSON.stringify(sizing));
await evaluate('document.querySelector(".remote-route-card").click()');
if(!await evaluate('document.querySelector("#remote-grid-dialog").open&&document.querySelector("#remote-specialist-grid").textContent.includes("coding")&&document.querySelector("#remote-specialist-grid").textContent.includes("<img")&&!document.querySelector("#remote-specialist-grid img")'))throw Error('remote drilldown');
await evaluate('document.querySelector("#remote-grid-close").click()');
await cdp('Emulation.setDeviceMetricsOverride' ,{width:390,height:844,deviceScaleFactor:1,mobile:false});
if(await evaluate('document.documentElement.scrollWidth>document.documentElement.clientWidth'))throw Error('mobile overflow');
await evaluate('document.querySelector(".remote-route-section").scrollIntoView()');
if(process.env.NEXUS_ROUTING_SCREENSHOT){const fs=await import('node:fs');const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_ROUTING_SCREENSHOT,Buffer.from(shot.data,'base64'));}
await evaluate('fetch("/fixture/empty").then(()=>document.querySelector("#refresh-routing").click())');
await eventually('document.querySelector("#remote-route-status").textContent.includes("No remote systems")','empty state absent');
if(await evaluate('document.querySelector("#remote-route-grid").children.length'))throw Error('stale paired system retained');
await evaluate('fetch("/fixture/fail").then(()=>document.querySelector("#refresh-routing").click())');
await eventually('document.querySelector("#remote-route-status").textContent.includes("could not be loaded")','failure state absent');
if(await evaluate('document.querySelector("#specialist-grid").children.length!==14||document.body.textContent.includes("private error")'))throw Error('failure affected specialists or leaked response');
socket.close();
`
