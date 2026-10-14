package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChromeChatFilesPreviewDownloadAndReload(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var picture bytes.Buffer
	png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	if path := os.Getenv("NEXUS_CHAT_FILES_IMAGE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		picture.Reset()
		picture.Write(b)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/session/csrf" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":1,"csrf_token":"fixture"}`))
			return
		}
		if r.URL.Path == "/app/api/v1/chats/chat/messages" {
			writeChromeJSON(w, map[string]any{"version": 1, "chat_id": "chat", "task_id": "task", "head_revision": 1, "messages": []any{map[string]any{"id": "message", "role": "assistant", "text": "Here are your generated files.", "revision": 1, "source_revision": 1}}, "next_cursor": "", "has_more": false})
			return
		}
		if r.URL.Path == "/app/api/v1/chats/chat/files" {
			writeChromeJSON(w, ChatFilePage{Version: 1, ChatID: "chat", Files: []ChatFile{{ID: "image", TaskID: "task", Name: "car.png", MediaType: "image/png", Size: picture.Len()}, {ID: "notes", TaskID: "task", Name: "notes.html", MediaType: "text/plain", Size: 25}}})
			return
		}
		if r.URL.Path == "/app/api/v1/chats/empty/files" {
			writeChromeJSON(w, ChatFilePage{Version: 1, ChatID: "empty", Files: []ChatFile{}})
			return
		}
		if r.URL.Path == "/app/api/v1/chats/chat/files/image/preview" {
			w.Header().Set("Content-Type", "image/png")
			w.Write(picture.Bytes())
			return
		}
		if r.URL.Path == "/app/api/v1/chats/chat/files/notes/download" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="notes.html"`)
			w.Write([]byte("<script>not run</script>\n"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app/api/") {
			http.NotFound(w, r)
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	downloadDir := t.TempDir()
	directory, _ := json.Marshal(downloadDir)
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
await cdp('Page.addScriptToEvaluateOnNewDocument',{source:'Object.defineProperty(window, "EventSource", {value:class {readyState=1; addEventListener(){} close(){} }, configurable:false});'});
await cdp('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:` + string(directory) + `});
await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});
await cdp('Page.navigate',{url:origin+'/app/chats/chat'});
await eventually('document.querySelectorAll(".chat-file-card").length===2','attachments did not load with chat');
await eventually('document.querySelector(".chat-file-preview img").naturalWidth>0','inline image unavailable');
if(!await evaluate('document.querySelector("#chat-files").closest(".transcript-panel")!==null'))throw Error('files outside chat');
await evaluate('window.fileImage=document.querySelector(".chat-file-preview img");document.querySelector(".chat-file-download").focus();window.NexusChatFiles.refresh()');
await new Promise(r=>setTimeout(r,250));
if(!await evaluate('window.fileImage===document.querySelector(".chat-file-preview img")&&document.activeElement.classList.contains("chat-file-download")'))throw Error('refresh replaced image or keyboard focus');
await evaluate('document.querySelectorAll(".chat-file-download")[1].click()');
const fs=await import('node:fs');const deadline=Date.now()+5000;let file=` + string(directory) + `+'/notes.html';while(!fs.existsSync(file)&&Date.now()<deadline)await new Promise(r=>setTimeout(r,50));
if(!fs.existsSync(file)||fs.readFileSync(file,'utf8')!=='<script>not run</script>\n')throw Error('download bytes changed or missing');
await cdp('Page.reload',{ignoreCache:true});
await eventually('document.querySelectorAll(".chat-file-card").length===2&&document.querySelector(".chat-file-preview img").naturalWidth>0','reload lost files');
await evaluate('document.querySelector("#chat-files").scrollIntoView({block:"start"})');
if(process.env.NEXUS_CHAT_FILES_SCREENSHOT){const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_CHAT_FILES_SCREENSHOT+'.desktop.png',Buffer.from(shot.data,'base64'));}
await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('mobile overflow');
await evaluate('document.querySelector("#chat-files").scrollIntoView({block:"start"})');
if(process.env.NEXUS_CHAT_FILES_SCREENSHOT){const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_CHAT_FILES_SCREENSHOT,Buffer.from(shot.data,'base64'));}
// Long messages, generated files and controls must never displace the composer.
await evaluate("window.longMessages=Array.from({length:30},(_,i)=>({id:'long-'+i,role:'assistant',text:'Long conversation message '.repeat(100)}));window.NexusChatRender.messages(document.querySelector('#transcript'),longMessages,true);document.querySelector('#feedback-panel').hidden=false;document.querySelector('#task-controls').hidden=false;document.querySelector('#composer-text').value='Keep my draft';");
for(const [width,height] of [[1440,1000],[1024,700],[700,700],[390,844],[390,480]]){
 await cdp('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false});
 await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
 for(const end of [false,true]){
  await evaluate("document.querySelector('.conversation-scroll').scrollTop="+(end?"100000":"0")+"");
  if(!await evaluate("(()=>{const input=document.querySelector('#composer-text').getBoundingClientRect(),send=document.querySelector('#send-message').getBoundingClientRect();return input.top>=0&&input.bottom<=innerHeight&&send.top>=0&&send.bottom<=innerHeight&&send.right<=innerWidth&&document.documentElement.scrollWidth<=innerWidth&&document.documentElement.scrollHeight<=innerHeight+1;})()"))throw Error('composer outside viewport '+width+'x'+height+' '+await evaluate("JSON.stringify({input:document.querySelector('#composer-text').getBoundingClientRect(),send:document.querySelector('#send-message').getBoundingClientRect(),page:document.documentElement.scrollHeight,panel:document.querySelector('.transcript-panel').getBoundingClientRect(),main:document.querySelector('main').getBoundingClientRect(),scroll:{box:document.querySelector('.conversation-scroll').getBoundingClientRect(),overflow:getComputedStyle(document.querySelector('.conversation-scroll')).overflow,html:document.querySelector('.conversation-scroll').children.length},parent:document.querySelector('#transcript').parentElement.className})"));
 }
}
await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});
await evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
await evaluate("const viewport=document.querySelector('.conversation-scroll');viewport.scrollTop=350;window.savedConversationTop=viewport.scrollTop;document.querySelector('#composer-text').disabled=false;document.querySelector('#composer-text').focus();window.NexusChatRender.messages(document.querySelector('#transcript'),longMessages.concat([{id:'last',role:'assistant',text:'New result'}]),true);");
if(!await evaluate("Math.abs(document.querySelector('.conversation-scroll').scrollTop-window.savedConversationTop)<2&&document.activeElement.id==='composer-text'&&document.querySelector('#composer-text').value==='Keep my draft'"))throw Error('refresh moved conversation or lost composer draft/focus '+await evaluate("JSON.stringify({top:document.querySelector('.conversation-scroll').scrollTop,saved:window.savedConversationTop,focus:document.activeElement.id,draft:document.querySelector('#composer-text').value})"));
if(process.env.NEXUS_CHAT_FILES_SCREENSHOT){const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_CHAT_FILES_SCREENSHOT+'.composer.png',Buffer.from(shot.data,'base64'));}
await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:480,deviceScaleFactor:1,mobile:false});
await evaluate("document.querySelector('#host-options').hidden=false;document.querySelector('#host-options').textContent='Choose a model';document.querySelector('#host-options').style.height='8rem';document.querySelector('#host-routing').hidden=false;document.querySelector('#host-receipt').hidden=false;document.querySelector('#host-receipt').textContent='Saved request details '.repeat(80);");
if(!await evaluate("(()=>{const input=document.querySelector('#composer-text').getBoundingClientRect(),send=document.querySelector('#send-message').getBoundingClientRect();return input.top>=0&&send.bottom<=innerHeight;})()"))throw Error('routing details displaced composer');
if(process.env.NEXUS_CHAT_FILES_SCREENSHOT){const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_CHAT_FILES_SCREENSHOT+'.composer-mobile.png',Buffer.from(shot.data,'base64'));}

await evaluate('window.NexusChatFiles.select("empty")');await new Promise(r=>setTimeout(r,200));
if(!await evaluate('document.querySelectorAll(".chat-file-card").length===0&&document.querySelector("#chat-files").hidden'))throw Error('old chat files leaked');
socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
}
