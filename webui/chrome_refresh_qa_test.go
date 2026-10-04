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

func TestChromeWorkboardRefreshRetainsVisibleSnapshot(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	page, snapshot := populatedChromeWorkboardFixture(false)
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/fail":
			fail.Store(true)
			return
		case "/app/api/v1/workboards":
			if fail.Load() {
				time.Sleep(500 * time.Millisecond)
				http.Error(w, "unavailable", 503)
				return
			}
			writeChromeJSON(w, page)
			return
		case "/app/api/v1/workboards/board-a":
			if fail.Load() {
				time.Sleep(500 * time.Millisecond)
				http.Error(w, "unavailable", 503)
				return
			}
			writeChromeJSON(w, snapshot)
			return
		case "/app/api/v1/workboards/board-a/events":
			w.WriteHeader(204)
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
 await cdp('Page.navigate',{url:origin+'/app/workboards/board-a'});
 await eventually('document.querySelector(".kanban-card")!==null','initial cards absent');
 await evaluate('window.beforeCards=document.querySelectorAll(".kanban-card").length;fetch("/fixture/fail")');
 await evaluate('document.querySelector("#refresh-workboards").click()');
 if(!await evaluate('document.querySelectorAll(".kanban-card").length===window.beforeCards&&!document.querySelector("#kanban").hidden'))throw Error('refresh blanked cards while request pending');
 await eventually('document.querySelector("#workboard-state").textContent.includes("could not")','failure not surfaced');
 if(!await evaluate('document.querySelectorAll(".kanban-card").length===window.beforeCards&&!document.querySelector("#kanban").hidden'))throw Error('failed refresh erased loaded cards');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}

func TestChromeChatRefreshFailureRetainsContent(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/fail":
			fail.Store(true)
			return
		case "/fixture/recover":
			fail.Store(false)
			return
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
			return
		case "/app/api/v1/operations":
			w.Write([]byte(`{"version":1,"items":[],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats":
			if fail.Load() {
				time.Sleep(300 * time.Millisecond)
				http.Error(w, "unavailable", 503)
				return
			}
			w.Write([]byte(`{"version":1,"items":[{"version":1,"chat_id":"chat-fixture","state":"completed","started_at":"2026-10-01T12:00:00Z"}],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats/chat-fixture/messages":
			if fail.Load() {
				time.Sleep(300 * time.Millisecond)
				http.Error(w, "unavailable", 503)
				return
			}
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: "chat-fixture", TaskID: "task-fixture", HeadRevision: 1, Messages: []HistoryMessage{{ID: "message-fixture", Role: "user", Text: "A synthetic chat description", Revision: 1, SourceRevision: 1}}})
			return
		case "/app/api/v1/chats/chat-fixture/events":
			w.WriteHeader(204)
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
 await cdp('Page.navigate',{url:origin+'/app/chats/chat-fixture'});
 await eventually('document.querySelectorAll("#transcript .message").length===1&&document.querySelector(".chat-name")?.textContent==="A synthetic chat description"','chat failed to load');
 await evaluate('window.messageBefore=document.querySelector("#transcript .message");window.chatBefore=document.querySelector("[data-chat-id]")');
 await evaluate('fetch("/fixture/fail")');
 await evaluate('window.NexusLive.wake()');
 await new Promise(resolve=>setTimeout(resolve,700));
 if(!await evaluate('document.querySelectorAll("#transcript .message").length===1&&document.querySelector(".chat-name")?.textContent==="A synthetic chat description"'))throw Error('failed refresh erased chat content');
 await evaluate('fetch("/fixture/recover")');
 await evaluate('window.NexusLive.wake()');
 await new Promise(resolve=>setTimeout(resolve,300));
 if(!await evaluate('document.querySelectorAll("#transcript .message").length===1&&document.querySelectorAll("[data-chat-id]").length===1'))throw Error('recovery duplicated content');
 if(!await evaluate('document.querySelector("#transcript .message")===window.messageBefore&&document.querySelector("[data-chat-id]")===window.chatBefore'))throw Error('refresh replaced stable chat nodes');
 await evaluate('window.fixtureMessages=Array.from({length:40},(_,i)=>({id:"synthetic-"+i,role:"assistant",text:"Synthetic scrolling message "+i+" text".repeat(80)}));window.NexusChatRender.messages(document.querySelector("#transcript"),fixtureMessages,true);document.querySelector("#transcript").style.height="300px";document.querySelector("#transcript").style.flex="none";document.querySelector("#transcript").scrollTop=350;window.savedTop=document.querySelector("#transcript").scrollTop;window.savedMessage=document.querySelector("#transcript").children[2]');
 await evaluate('window.NexusChatRender.messages(document.querySelector("#transcript"),fixtureMessages.concat([{id:"new",role:"assistant",text:"New streamed result"}]),true)');
 if(!await evaluate('Math.abs(document.querySelector("#transcript").scrollTop-savedTop)<2&&document.querySelector("#transcript").children[2]===savedMessage'))throw Error('background update moved reading position');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
