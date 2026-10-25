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
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeChatActivityLocalAndSparkLifecycle(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var submits, cancels atomic.Int32
	var phase atomic.Value
	phase.Store("running")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := phase.Load().(string)
		taskState := state
		if state == "writing" {
			taskState = "running"
		}
		switch r.URL.Path {
		case "/fixture/state":
			phase.Store(r.URL.Query().Get("state"))
			w.WriteHeader(204)
			return
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
			return
		case "/app/api/v1/operations":
			w.Write([]byte(`{"version":1,"items":[],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/submissions/submission-fixture":
			writeChromeJSON(w, map[string]any{"version": 1, "submission_id": "submission-fixture", "state": state, "can_cancel": state == "queued", "created_at": "2026-10-22T12:00:00Z", "updated_at": "2026-10-22T12:00:00Z"})
			return
		case "/app/api/v1/submissions/submission-fixture/cancel":
			cancels.Add(1)
			phase.Store("canceled")
			writeChromeJSON(w, map[string]any{"version": 1, "operation_id": "cancel-fixture", "target_kind": "submission", "target_id": "submission-fixture", "requested": true, "state": "canceled"})
			return
		case "/app/api/v1/chats":
			if r.Method == http.MethodPost {
				submits.Add(1)
				writeChromeJSON(w, map[string]any{"version": 1, "operation_id": "submit-fixture", "submission_id": "submission-fixture", "state": "queued"})
				return
			}
			writeChromeJSON(w, map[string]any{"version": 1, "items": []any{map[string]any{"version": 1, "chat_id": "chat-fixture", "state": taskState, "started_at": "2026-10-22T12:00:00Z"}}, "has_more": false, "next_cursor": ""})
			return
		case "/app/api/v1/chats/chat-fixture/messages":
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: "chat-fixture", TaskID: "task-fixture", HeadRevision: 1, Messages: []HistoryMessage{{ID: "message-fixture", Role: "user", Text: "Please prepare a reply", Revision: 1, SourceRevision: 1}}})
			return
		case "/app/api/v1/tasks/task-fixture/controls":
			active := state == "running" || state == "writing" || state == "queued"
			writeChromeJSON(w, map[string]any{"version": 1, "task_id": "task-fixture", "revision": 1, "can_resume": !active, "can_steer": active, "can_cancel": active, "can_feedback": !active})
			return
		case "/app/api/v1/tasks/task-fixture/approvals":
			writeChromeJSON(w, ApprovalPage{Version: 1, TaskID: "task-fixture", Items: []ApprovalSummary{}})
			return
		case "/app/api/v1/chats/chat-fixture/events":
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			last := ""
			revision := 0
			for {
				next := phase.Load().(string)
				if next != last {
					last = next
					revision++
					kind, durability := "lifecycle.event", "committed"
					rev := revision
					data := map[string]any{"task_id": "task-fixture", "state": next}
					if next == "writing" {
						kind, durability = "chat.delta", "provisional"
						rev = 0
						data = map[string]any{"task_id": "task-fixture", "text": "A partial reply"}
					}
					payload, _ := json.Marshal(map[string]any{"version": 1, "kind": kind, "subject": "chat-fixture", "durability": durability, "revision": rev, "cursor": fmt.Sprint("fixture:", revision), "data": data})
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, payload)
					f.Flush()
				}
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
		case "/app/api/v1/remote-task-control":
			writeChromeJSON(w, map[string]any{"version": 1, "instance": "spark", "request_id": "request-existing-0001", "submission_id": "submission-fixture", "state": state, "cancel_requested": false, "task_ids": []string{}})
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
 const change=async state=>evaluate('fetch("/fixture/state?state='+state+'")');
 await cdp('Page.navigate',{url:origin+'/app/chats/chat-fixture'});
 await eventually('!document.querySelector("#chat-activity").hidden&&document.querySelector("#chat-activity-label").textContent==="Working…"','running activity missing');
 for(const [width,height] of [[1440,900],[390,844],[320,568],[844,390]]){
  await cdp('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false});
  if(process.env.NEXUS_ACTIVITY_SCREENSHOT&&[1440,390].includes(width)){const shot=await cdp('Page.captureScreenshot',{format:'png'});(await import('node:fs')).writeFileSync(process.env.NEXUS_ACTIVITY_SCREENSHOT+'-'+width+'.png',Buffer.from(shot.data,'base64'));}
  if(!await evaluate('(()=>{const a=document.querySelector("#chat-activity").getBoundingClientRect(),b=document.querySelector("#composer-text").getBoundingClientRect();return a.top>=0&&a.bottom<=b.top&&b.bottom<=innerHeight&&document.documentElement.scrollWidth<=innerWidth})()'))throw Error('activity/composer layout '+width);
 }
 await cdp('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
 if(!await evaluate('getComputedStyle(document.querySelector(".chat-activity-orb")).animationName==="none"'))throw Error('reduced motion ignored');
 await change('writing');await eventually('document.querySelector("#chat-activity-label").textContent==="Responding…"','streaming activity missing');
 for(const state of ['completed','failed','canceled']){await change(state);await eventually('document.querySelector("#chat-activity").hidden','terminal activity retained '+state);await change('running');await eventually('!document.querySelector("#chat-activity").hidden','running activity not restored');}
 await evaluate('window.activityBeforeReload=true');await cdp('Page.reload');await eventually('!window.activityBeforeReload&&!document.querySelector("#chat-activity").hidden','reload lost running state');
 await eventually('!document.querySelector("#new-chat").disabled','new chat not ready after reload');await evaluate('document.querySelector("#new-chat").click()');await eventually('location.pathname==="/app/chats"&&document.querySelector("#chat-activity").hidden','new chat retained activity');
 await change('queued');await eventually('!document.querySelector("#send-message").disabled','composer did not become ready');
 await evaluate('document.querySelector("#composer-text").value="Queued fixture";document.querySelector("#composer").requestSubmit()');await eventually('document.querySelector("#chat-activity-label").textContent==="Waiting to start…"&&!document.querySelector("#cancel-task").disabled','queued submission missing');
 await evaluate('window.confirm=()=>true;document.querySelector("#cancel-task").click()');await eventually('document.querySelector("#chat-activity").hidden','queued cancellation retained activity');
 await change('queued');await evaluate('history.replaceState(null,"","/app/chats#chat_host=spark&chat_request=request-existing-0001")');await cdp('Page.reload');
 try{await eventually('document.querySelector("#chat-activity-label").textContent==="Waiting to start…"','Spark queue missing');}catch(e){throw Error(e.message+JSON.stringify(await evaluate('({url:location.href,activity:document.querySelector("#chat-activity").outerHTML,receipt:document.querySelector("#host-receipt").textContent,notice:document.querySelector("#host-notice").textContent,active:window.NexusHostMentions.active()})')));}
 await change('running');await evaluate('document.querySelector(".remote-task-card>button").click()');await eventually('document.querySelector("#chat-activity-label").textContent==="Working…"','Spark running missing');
 await change('succeeded');await evaluate('document.querySelector(".remote-task-card>button").click()');await eventually('document.querySelector("#chat-activity").hidden','Spark completion retained activity');
 await evaluate('document.querySelector("#host-new-task").click()');await change('running');await evaluate('document.querySelector(".remote-task-card>button").click()');await eventually('document.querySelector(".remote-task-card>output").textContent.includes("running")','old remote status not observed');
 if(!await evaluate('document.querySelector("#chat-activity").hidden'))throw Error('old remote request reactivated new-chat activity');
 if(!firefox){await cdp('Browser.close');}socket.close();`
	if out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if submits.Load() != 1 || cancels.Load() != 1 {
		t.Fatal("unexpected submission/cancellation count", submits.Load(), cancels.Load())
	}
}
