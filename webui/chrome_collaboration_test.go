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

func TestChromeModelCollaborationPage(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/collaboration" {
			count.Add(1)
			if r.URL.Query().Get("topic") == "fail" {
				http.Error(w, "private error", 503)
				return
			}
			m := CollaborationMessage{Sequence: 4, SentAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), Hostname: "spark-host", Harness: "pi", HarnessID: "pi-coder", Runner: "Ollama", Provider: "ollama", SenderID: "qwen", SenderModel: "Qwen Coder", Recipient: "laguna", Topic: "parser", TaskID: "task-one", SessionID: "session-one", Text: "<img src=x onerror=alert(1)>\nCheck the parser bounds.", Private: true}
			next := int64(3)
			if r.URL.Query().Get("before") != "" {
				m.Sequence = 2
				next = 0
			}
			writeChromeJSON(w, CollaborationPage{Version: 1, Enabled: true, Messages: []CollaborationMessage{m}, NextBefore: next})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app/api/") {
			http.Error(w, "Unexpected chat request", 500)
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
await cdp('Page.navigate',{url:origin+'/app/collaboration'});
await eventually('document.querySelectorAll(".collaboration-message").length===1','messages did not load');
if(!await evaluate('!document.querySelector("#collaboration-view").hidden&&document.querySelector("#chat-view").hidden&&document.querySelector("[data-view=collaboration]").getAttribute("aria-current")==="page"'))throw Error('wrong route');
if(!await evaluate('document.querySelector(".collaboration-message").textContent.includes("spark-host")&&document.querySelector(".collaboration-message").textContent.includes("pi-coder")&&document.querySelector(".collaboration-message").textContent.includes("Runner: Ollama")&&document.querySelector(".collaboration-message").textContent.includes("Qwen Coder")&&document.querySelector("time").dateTime==="2026-10-09T12:00:00Z"&&!document.querySelector(".collaboration-message img")'))throw Error('provenance or safe text missing');
await evaluate('window.row=document.querySelector(".collaboration-message");document.querySelector("#collaboration-topic").value="draft";document.querySelector("#collaboration-refresh").click()');
await eventually('!document.querySelector("#collaboration-refresh").disabled','refresh pending');
if(!await evaluate('document.querySelector(".collaboration-message")===row&&document.querySelector("#collaboration-topic").value==="draft"'))throw Error('refresh lost row or draft');
await evaluate('document.querySelector("#collaboration-older").click()');await eventually('document.querySelectorAll(".collaboration-message").length===2','older messages missing');
if(process.env.NEXUS_COLLAB_SCREENSHOT){const fs=await import('node:fs');const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_COLLAB_SCREENSHOT+'-desktop.png',Buffer.from(shot.data,'base64'));}
await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('mobile overflow');
if(process.env.NEXUS_COLLAB_SCREENSHOT){const fs=await import('node:fs');const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_COLLAB_SCREENSHOT+'-mobile.png',Buffer.from(shot.data,'base64'));}
await evaluate('document.querySelector("#collaboration-topic").value="fail";document.querySelector("#collaboration-filter").dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}))');await eventually('document.querySelector("#collaboration-status").textContent.includes("may be stale")','failed refresh unmarked');if(await evaluate('document.body.textContent.includes("private error")'))throw Error('private error leaked');socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
	if count.Load() < 4 {
		t.Fatal("missing browser interactions", count.Load())
	}
}
