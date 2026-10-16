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

func TestChromeRemoteSidebarReloadAndLocalSwitchDoNotDispatch(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/api/v1/session/csrf":
			w.Write([]byte(`{"version":1,"csrf_token":"fixture"}`))
			return
		case "/app/api/v1/operations":
			w.Write([]byte(`{"version":1,"items":[],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats":
			w.Write([]byte(`{"version":1,"items":[{"version":1,"chat_id":"local-chat","title":"Local chat","state":"completed","started_at":"2026-10-01T12:00:00Z"}],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats/local-chat/messages":
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: "local-chat", TaskID: "local-task", HeadRevision: 1, Messages: []HistoryMessage{{ID: "local-message", Role: "user", Text: "LOCAL_HISTORY", Revision: 1, SourceRevision: 1}}})
			return
		case "/app/api/v1/tasks/local-task/controls":
			w.Write([]byte(`{"version":1,"task_id":"local-task","revision":1,"can_resume":true,"can_steer":false,"can_cancel":false,"can_feedback":false}`))
			return
		case "/app/api/v1/chats/local-chat/events":
			w.WriteHeader(204)
			return
		case "/app/api/v1/remote-chats":
			w.Write([]byte(`{"version":1,"items":[{"sequence":2,"instance":"spark","request_id":"request-child-0001","root":"request-parent-001","title":"Saved Spark conversation","model":"Vision model","created_at":"2026-10-15T12:00:00Z"}],"next_cursor":"","indexing":false}`))
			return
		case "/app/api/v1/remote-chats/spark/request-child-0001":
			w.Write([]byte(`{"version":1,"instance":"spark","request_id":"request-child-0001","cached":true,"state":"succeeded","conversation":[{"role":"user","text":"Saved Spark conversation","request_id":"request-parent-001","model":"Vision model","host":"spark"},{"role":"assistant","text":"EARLIER_FACT","request_id":"request-parent-001","model":"Vision model","host":"spark"},{"role":"user","text":"Follow-up","request_id":"request-child-0001","model":"Vision model","host":"spark"},{"role":"assistant","text":"FINAL_FACT","request_id":"request-child-0001","model":"Vision model","host":"spark"}]}`))
			return
		case "/app/api/v1/remote-task-control":
			http.Error(w, "offline", 503)
			return
		case "/app/api/v1/remote-files/spark/request-parent-001":
			w.Write([]byte(`{"version":1,"instance":"spark","request_id":"request-parent-001","files":[{"id":"file_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"scan.pdf","media_type":"application/octet-stream"}]}`))
			return
		case "/app/api/v1/remote-files/spark/request-child-0001":
			w.Write([]byte(`{"version":1,"instance":"spark","request_id":"request-child-0001","files":[{"id":"file_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"notes.txt","media_type":"application/octet-stream"}]}`))
			return
		case "/app/api/v1/remote-dispatch":
			dispatches.Add(1)
			http.Error(w, "unexpected dispatch", 500)
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.ReplaceAll(strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0], "result.exceptionDetails.text", "JSON.stringify(result.exceptionDetails)") + `
 await cdp('Page.navigate',{url:origin+'/app/chats'});await eventually('document.querySelectorAll("#chat-list>li").length===2&&!!document.querySelector("[data-remote-chat]")','Shared sidebar missing Spark/local rows');
 await evaluate('document.querySelector("[data-remote-chat]").click()');await eventually('document.querySelector("#host-receipt").textContent.includes("FINAL_FACT")&&document.querySelectorAll("#host-receipt .chat-file-card").length===2','Sidebar reopen lost saved history/files');
 await eventually('document.querySelector("[data-remote-chat]")?.getAttribute("aria-current")==="true"&&document.querySelector("#chat-title").textContent==="Saved Spark conversation"','Spark selection/title missing');
 await cdp('Page.reload');await eventually('document.querySelector("#host-receipt").textContent.includes("FINAL_FACT")&&document.querySelectorAll("#host-receipt .chat-file-card").length===2','Reload lost Spark history/files');
 await eventually('document.querySelector("#host-receipt a")?.href.includes("/remote-files/spark/")&&document.querySelector("#host-notice").textContent.includes("Reconnect")','Offline history granted continuation or lost download scope');
 await evaluate('window.NexusRemoteEvents.attach(document.querySelector(".remote-task-card"),"spark","request-child-0001","fixture-progress","/app","fixture")');
 for(const width of [1440,390]){
  await cdp('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:false});
  await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
  if(process.env.NEXUS_UI_QA_SCREENSHOTS){const shot=await cdp('Page.captureScreenshot',{format:'png'});(await import('node:fs')).writeFileSync('/tmp/nexus-remote-buttons-'+width+'.png',Buffer.from(shot.data,'base64'));}
  if(!await evaluate("(()=>{const card=document.querySelector('.remote-task-card'),buttons=[...card.querySelectorAll('button')].filter(b=>b.getClientRects().length>0),refresh=buttons[0],cancel=buttons[1],a=refresh.getBoundingClientRect(),b=cancel.getBoundingClientRect();refresh.focus();return buttons.every(button=>{const r=button.getBoundingClientRect();return r.height>=32&&r.right<=innerWidth&&r.left>=0;})&&(b.top>=a.bottom||b.left-a.right>=6)&&getComputedStyle(refresh).outlineStyle==='solid'&&getComputedStyle(card.querySelector('output')).display==='block'&&cancel.disabled&&getComputedStyle(card.querySelector('.remote-cancel-confirmation')).display==='none';})()"))throw Error('Remote controls layout '+width+' '+JSON.stringify(await evaluate("[...document.querySelectorAll('.remote-task-card button')].map(b=>({text:b.textContent,rect:b.getBoundingClientRect().toJSON(),outline:getComputedStyle(b).outlineStyle,disabled:b.disabled}))")));
 }
 await evaluate('document.querySelector("button[data-chat-id=local-chat]").click()');await eventually('document.querySelector("#transcript").textContent.includes("LOCAL_HISTORY")&&document.querySelector("#host-receipt").hidden','Local switch retained Spark context');
 await evaluate('window.dispatchEvent(new CustomEvent("nexus-chat-preference",{detail:{id:"local-chat"}}))');await eventually('!!document.querySelector("[data-remote-chat]")&&document.querySelectorAll("#chat-list>li").length===2','Local refresh erased Spark sidebar row');
 await evaluate('document.querySelector("[data-remote-chat]").click()');await eventually('document.querySelector("#host-receipt").textContent.includes("EARLIER_FACT")','Reopen after local switch failed');
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});if(await evaluate('document.documentElement.scrollWidth>innerWidth'))throw Error('Narrow sidebar overflow');socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
	if dispatches.Load() != 0 {
		t.Fatal("sidebar/reload dispatched inference")
	}
}
