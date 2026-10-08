package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestChromeChatRenameAndPinSurviveRefresh(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	prefs := map[string]sessions.ChatPreference{}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/fixture/external-title":
			p := prefs["old"]
			p.Title = "Changed elsewhere"
			p.Revision++
			prefs["old"] = p
			writeChromeJSON(w, p)
			return
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
			return
		case "/app/api/v1/operations":
			writeChromeJSON(w, map[string]any{"version": 1, "items": []any{}, "has_more": false, "next_cursor": ""})
			return
		case "/app/api/v1/chats":
			ids := []string{"new", "old"}
			if prefs["old"].Pinned {
				ids = []string{"old", "new"}
			}
			items := []map[string]any{}
			for _, id := range ids {
				p := prefs[id]
				items = append(items, map[string]any{"version": 1, "chat_id": id, "title": p.Title, "pinned": p.Pinned, "preference_revision": p.Revision, "state": "completed", "started_at": "2026-10-08T12:00:00Z"})
			}
			writeChromeJSON(w, map[string]any{"version": 1, "items": items, "has_more": false, "next_cursor": ""})
			return
		case "/app/api/v1/chat-preferences":
			var input sessions.ChatPreferenceUpdate
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Validate() != nil || r.Header.Get("X-Darwin-CSRF") != "fixture" {
				http.Error(w, "invalid", 400)
				return
			}
			p := prefs[input.ChatID]
			if p.Revision != input.ExpectedRevision {
				http.Error(w, "conflict", 409)
				return
			}
			if input.Title != nil {
				p.Title = *input.Title
			}
			if input.Pinned != nil {
				p.Pinned = *input.Pinned
			}
			p.Revision++
			prefs[input.ChatID] = p
			writes++
			writeChromeJSON(w, map[string]any{"version": 1, "chat_id": input.ChatID, "preference": p})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app/api/v1/chat-preferences/") {
			id := strings.TrimPrefix(r.URL.Path, "/app/api/v1/chat-preferences/")
			writeChromeJSON(w, map[string]any{"version": 1, "chat_id": id, "preference": prefs[id]})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app/api/v1/chats/"), "/messages")
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: id, TaskID: "task-" + id, HeadRevision: 1, Messages: []HistoryMessage{{ID: "message-" + id, Role: "user", Text: "Original " + id, Revision: 1, SourceRevision: 1}}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.WriteHeader(204)
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
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
 await cdp('Page.navigate',{url:origin+'/app/chats/old'});
 await eventually('document.querySelectorAll(".chat-more").length===2&&document.querySelector("#chat-title").textContent==="Original old"','chats unavailable');
 await evaluate('window.oldRow=()=>document.querySelector("[data-chat-id=old]").parentElement;oldRow().querySelector(".chat-more").click()');
 if(!await evaluate('oldRow().querySelector(".chat-more").getAttribute("aria-expanded")==="true"&&document.activeElement.textContent==="Rename"'))throw Error('menu keyboard focus missing');
 await evaluate('oldRow().querySelector("[role=menuitem]").click()');
 await eventually('document.querySelector("dialog.chat-rename-dialog")?.open','rename dialog absent');
 await evaluate('document.querySelector("#chat-rename-input").value="My renamed chat";document.querySelector("#chat-rename-input").dispatchEvent(new Event("input"));window.NexusLive.wake()');
 await new Promise(r=>setTimeout(r,400));
 if(!await evaluate('document.querySelector("#chat-rename-input").value==="My renamed chat"&&document.activeElement.id==="chat-rename-input"'))throw Error('refresh lost rename draft');
 await evaluate('document.querySelector("dialog.chat-rename-dialog form").requestSubmit()');
 await eventually('!document.querySelector("dialog.chat-rename-dialog")&&oldRow().querySelector(".chat-name").textContent==="My renamed chat"&&document.querySelector("#chat-title").textContent==="My renamed chat"','rename did not reconcile').catch(async e=>{throw Error(e.message+JSON.stringify(await evaluate('({title:document.querySelector("#chat-title").textContent,row:oldRow().innerText,dialog:document.querySelector("dialog.chat-rename-dialog")?.innerText})')))});
 await evaluate('oldRow().querySelector(".chat-more").click();oldRow().querySelector("[role=menu]").lastElementChild.click()');
 await eventually('document.querySelector("#chat-list").firstElementChild.firstElementChild.dataset.chatId==="old"&&oldRow().dataset.pinned==="true"','pin not first');
 await evaluate('window.beforeReload=true');await cdp('Page.reload');
 await eventually('!window.beforeReload&&document.querySelector("#chat-list")?.firstElementChild?.firstElementChild.dataset.chatId==="old"&&document.querySelector("#chat-title").textContent==="My renamed chat"','saved preferences lost after reload');
 await evaluate('window.oldRow=()=>document.querySelector("[data-chat-id=old]").parentElement;oldRow().querySelector(".chat-more").click();oldRow().querySelector("[role=menu]").lastElementChild.click()');
 await eventually('document.querySelector("#chat-list").firstElementChild.firstElementChild.dataset.chatId==="new"&&oldRow().dataset.pinned==="false"','unpin did not restore order');
 await evaluate('oldRow().querySelector(".chat-more").click();oldRow().querySelector(".chat-actions").dispatchEvent(new KeyboardEvent("keydown",{key:"Escape",bubbles:true}))');
 if(!await evaluate('oldRow().querySelector("[role=menu]").hidden&&document.activeElement===oldRow().querySelector(".chat-more")'))throw Error('Escape failed to restore focus');
 if(process.env.NEXUS_CHAT_SCREENSHOT)(await import('node:fs')).writeFileSync(process.env.NEXUS_CHAT_SCREENSHOT+'-mobile.png',Buffer.from((await cdp('Page.captureScreenshot',{format:'png'})).data,'base64'));
 if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('mobile horizontal overflow');
 await cdp('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});
 if(!await evaluate('document.querySelectorAll(".chat-more").length===2'))throw Error('desktop controls absent');
 await evaluate('oldRow().querySelector(".chat-more").click()');
 if(process.env.NEXUS_CHAT_SCREENSHOT)(await import('node:fs')).writeFileSync(process.env.NEXUS_CHAT_SCREENSHOT+'-desktop.png',Buffer.from((await cdp('Page.captureScreenshot',{format:'png'})).data,'base64'));
 await evaluate('oldRow().querySelector("[role=menuitem]").click()');
 await eventually('document.querySelector("dialog.chat-rename-dialog")?.open','second rename dialog absent');
 await evaluate('document.querySelector("#chat-rename-input").value="Keep my draft";document.querySelector("#chat-rename-input").dispatchEvent(new Event("input"))');
 await evaluate('fetch("/fixture/external-title").then(r=>r.json()).then(p=>window.NexusChatDescriptions.decorate(oldRow().firstElementChild,{version:1,chat_id:"old",title:p.title,pinned:p.pinned,preference_revision:p.revision,state:"completed",started_at:"2026-10-08T12:00:00Z"}))');
 await evaluate('document.querySelector("dialog.chat-rename-dialog form").requestSubmit()');
 await eventually('document.querySelector("dialog.chat-rename-dialog [role=status]")?.textContent.includes("changed elsewhere")','concurrent rename was silently overwritten');
 if(!await evaluate('document.querySelector("#chat-rename-input").value==="Keep my draft"'))throw Error('conflict discarded draft');
 await evaluate('document.querySelector("dialog.chat-rename-dialog button[type=button]").click()');
 socket.close();`
	out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 3 {
		t.Fatal("unexpected mutation count", writes)
	}
}
