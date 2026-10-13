package webui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeComfyWorkflowControls(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var submits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/api/v1/comfy/jobs/saved/image":
			var b bytes.Buffer
			png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 16)))
			w.Header().Set("Content-Type", "image/png")
			w.Write(b.Bytes())
			return
		case "/app/api/v1/session/csrf":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":1,"csrf_token":"fixture"}`))
			return
		case "/app/api/v1/comfy":
			w.Write([]byte(`{"version":1,"jobs":[{"key":"saved","state":"accepted"}]}`))
			return
		case "/app/api/v1/comfy/jobs/waiting":
			w.Write([]byte(`{"version":1,"job":{"key":"waiting","state":"accepted"}}`))
			return
		case "/app/api/v1/comfy/jobs":
			if r.Method != "POST" || r.Header.Get("X-Darwin-CSRF") != "fixture" {
				t.Error("missing mutation authority")
			}
			submits.Add(1)
			http.Error(w, "response lost", 503)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app/api/v1/comfy/jobs/") {
			w.Write([]byte(`{"version":1,"job":{"key":"saved","state":"success","history":{"status":{"completed":true,"status_str":"success"},"outputs":{"2":{"images":[{"filename":"test.png","type":"output","subfolder":""}]}}}}}`))
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
await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false});
await cdp('Page.navigate',{url:origin+'/app/comfy'});
await eventually('document.querySelectorAll("#comfy-jobs button").length===1','saved jobs missing');
if(!await evaluate('window.NexusComfyApproval.preview({version:1,kind:"comfy_workflow",arguments:{key:"safe",workflow:{"1":{class_type:"EmptyImage",inputs:{}}}}},"comfy_submit",131072).includes("EmptyImage")'))throw Error('chat workflow preview missing');
if(await evaluate('Boolean(window.NexusComfyApproval.preview({version:1,kind:"comfy_cancel",arguments:{key:"safe"}},"comfy_submit",131072))'))throw Error('wrong proposal accepted');
if(!await evaluate('!document.querySelector("#comfy-view").hidden&&document.querySelector("#chat-view").hidden&&document.querySelector("[data-view=comfy]").getAttribute("aria-current")==="page"'))throw Error('wrong route');
await evaluate('document.querySelector("#comfy-key").value="waiting";document.querySelector("#comfy-check").click()');
await eventually('!document.querySelector("#comfy-cancel").disabled','accepted job cannot be canceled');
await evaluate('document.querySelector("#comfy-key").value="other";document.querySelector("#comfy-key").dispatchEvent(new Event("input"))');
if(!await evaluate('document.querySelector("#comfy-cancel").disabled'))throw Error('edited key retained old cancellation target');
await evaluate('document.querySelector("#comfy-jobs button").click()');
await eventually('document.querySelector("#comfy-state").textContent.includes("success")','job check failed');
await eventually('document.querySelector("#comfy-images img")?.naturalWidth===16','image preview failed');
if(!await evaluate('document.querySelector("#comfy-images a").hasAttribute("download")'))throw Error('download missing');
await evaluate('(()=>{const d=new DataTransfer();d.items.add(new File([JSON.stringify({"1":{class_type:"EmptyImage",inputs:{}}})],"workflow.json",{type:"application/json"}));const f=document.querySelector("#comfy-file");f.files=d.files;f.dispatchEvent(new Event("change"));})()');
await eventually('!document.querySelector("#comfy-run").disabled','workflow validation failed');
await evaluate('document.querySelector("#comfy-run").click()');
await eventually('document.querySelector("#comfy-run").disabled','uncertain dispatch not blocked');
await new Promise(r=>setTimeout(r,300));
if(!await evaluate('document.querySelector("#comfy-key").value.startsWith("web-")'))throw Error('original key lost');
await evaluate('document.querySelector("#comfy-jobs button").click()');
await eventually('document.querySelector("#comfy-state").textContent.includes("success")','saved job recheck failed');
if(!await evaluate('document.querySelector("#comfy-run").disabled'))throw Error('another job cleared uncertain submission fence');
await evaluate('window.qaImage=document.querySelector("#comfy-images img");document.querySelector("#comfy-check").click()');
await new Promise(r=>setTimeout(r,300));
if(!await evaluate('window.qaImage===document.querySelector("#comfy-images img")'))throw Error('poll replaced unchanged preview');
await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('narrow overflow');
if(process.env.NEXUS_COMFY_SCREENSHOT){const fs=await import('node:fs');const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_COMFY_SCREENSHOT,Buffer.from(shot.data,'base64'));}
socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
	if submits.Load() != 1 {
		t.Fatal("submission retried", submits.Load())
	}
}
