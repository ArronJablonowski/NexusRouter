package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeLiveStatsAndStylesWithoutReload(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/advance" {
			stage.Store(1)
			return
		}
		if r.URL.Path == "/app/api/v1/stats" {
			count := map[string]any{"input": strconv.Itoa(10 + int(stage.Load())*20), "output": "5", "unknown": 0, "measured": 1, "partial": 0}
			meter := map[string]any{"lifetime": count, "trip": count, "revision": 0, "reset_at": ""}
			writeChromeJSON(w, map[string]any{"version": 1, "cloud": meter, "local": meter, "unclassified": 0, "updated_at": time.Now().UTC()})
			return
		}
		if r.URL.Path == "/app/assets/v1/app.css" && stage.Load() == 1 {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("ETag", "\"live-style-two\"")
			if r.Method != "HEAD" {
				fmt.Fprint(w, string(mustAsset(t, "assets/v1/app.css"))+"\nbody{--live-style-test:2}")
			}
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	preamble := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0]
	steps := `
 await cdp('Page.navigate',{url:origin+'/app/stats'});
 await eventually('document.querySelector(".stats-local output")?.getAttribute("aria-label").includes("10 tokens")','initial stats absent');
 await evaluate('window.sameDocument=123;const draft=document.createElement("textarea");draft.id="live-draft";draft.value="Do not overwrite";document.querySelector("#stats-view").append(draft);draft.focus();');
 await evaluate('fetch("/fixture/advance")');
 await eventually('document.querySelector(".stats-local output")?.getAttribute("aria-label").includes("30 tokens")','odometer failed automatic update');
 await evaluate('window.NexusLive.wake()');
 await eventually('getComputedStyle(document.body).getPropertyValue("--live-style-test").trim()==="2"','styles did not update in place');
 if(!await evaluate('window.sameDocument===123&&document.querySelector("#live-draft").value==="Do not overwrite"&&document.activeElement.id==="live-draft"'))throw Error('document reloaded or draft lost');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", preamble+steps, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}

func TestChromeLiveSettingsPreservesDirtyDraft(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/advance":
			stage.Add(1)
			return
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
			return
		case "/app/api/v1/settings":
			if r.Method == http.MethodPost {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			if stage.Load() == 2 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			access := map[string]any{"tools_enabled": false, "delegate_read_tools": false, "specialists_allow_cloud": false, "read_root": fmt.Sprintf("/fixture/%d", stage.Load()), "skills_enabled": false, "skills_auto_draft": false, "skills_root": "", "skills_scope": "project", "remote_advertisement": map[string]any{"enabled": false, "interface": "", "name": "", "ssh_port": 0}}
			writeChromeJSON(w, map[string]any{"version": 1, "digest": fmt.Sprintf("%064d", stage.Load()), "active": access, "saved": access, "restart_required": false})
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/settings'});
 await eventually('document.querySelector("#tools-read-root").value==="/fixture/0"','settings absent');
 await evaluate('document.querySelector("#tools-read-root").value="/my-unsaved-draft";document.querySelector("#tools-read-root").blur();');
 await evaluate('fetch("/fixture/advance").then(()=>window.NexusLive.wake())');
 await eventually('document.querySelector("#settings-status").textContent.includes("unsaved edits are preserved")','dirty settings not protected');
 if(!await evaluate('document.querySelector("#tools-read-root").value==="/my-unsaved-draft"'))throw Error('draft overwritten');
 await evaluate('document.querySelector("#tools-read-root").value="/fixture/0";window.NexusLive.wake()');
 await eventually('document.querySelector("#tools-read-root").value==="/fixture/1"','clean settings not reconciled');
 await evaluate('document.querySelector("#tools-read-root").value="/draft-keep";document.querySelector("#refresh-settings").click()');
 if(!await evaluate('document.querySelector("#tools-read-root").value==="/draft-keep"'))throw Error('manual refresh erased draft');
 await evaluate('document.querySelector("#save-settings").click()');
 await eventually('document.querySelector("#settings-status").textContent.includes("changed elsewhere")','conflict not displayed');
 if(!await evaluate('document.querySelector("#tools-read-root").value==="/draft-keep"'))throw Error('conflict erased draft');
 await evaluate('document.querySelector("#reset-settings").click();fetch("/fixture/advance")');
 await evaluate('document.querySelector("#refresh-settings").click()');
 await eventually('document.querySelector("#settings-status").textContent.includes("could not be loaded")','failed refresh absent');
 if(!await evaluate('document.querySelector("#tools-read-root").value==="/fixture/1"&&!document.querySelector("#save-settings").disabled'))throw Error('failed refresh discarded settings');
 for(const width of [1440,1024,390,319]) {
  await cdp('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false});
  if(!await evaluate('document.documentElement.scrollWidth<=innerWidth+1'))throw Error('settings horizontal overflow at '+width);
 }
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
