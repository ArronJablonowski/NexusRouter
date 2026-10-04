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

func TestChromeJobStatusAndOpenCardUpdateInPlace(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	var stage atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture/advance":
			stage.Add(1)
			return
		case "/app/api/v1/remote-membership":
			writeChromeJSON(w, map[string]any{"version": 1, "enabled": false})
			return
		case "/app/api/v1/jobs":
			items := []any{}
			if r.URL.Query().Get("kind") == "" {
				state, evidence := "unknown", "no_active_execution_lease"
				if stage.Load() > 0 {
					state, evidence = "failed", "submission_terminal_state"
				}
				items = append(items, map[string]any{"task_id": "task-a", "session_id": "chat-a", "state": "running", "started_at": "2026-09-21T00:00:00Z", "execution": map[string]any{"state": state, "evidence": evidence, "observed_at": "2026-10-04T00:00:00Z"}})
			}
			writeChromeJSON(w, map[string]any{"version": 1, "items": items, "has_more": false, "next_cursor": ""})
			return
		case "/app/api/v1/chats/chat-a/messages":
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: "chat-a", TaskID: "task-a", HeadRevision: 1, Messages: []HistoryMessage{{ID: "message-a", Role: "user", Text: "Synthetic status test", Revision: 1, SourceRevision: 1}}})
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
 await cdp('Page.navigate',{url:origin+'/app/active-jobs'});
 await eventually('document.querySelector("#local-jobs-list li span")?.textContent.includes("Needs attention")','historical head mislabeled running');
 await evaluate('window.row=document.querySelector("#local-jobs-list li");window.button=row.querySelector("button");button.click()');
 await eventually('document.querySelector(".job-detail-dialog").open','job card did not open');
 await evaluate('fetch("/fixture/advance")');
 await eventually('document.querySelector("#local-jobs-list li span")?.textContent.startsWith("Failed")','status did not automatically change');
 if(!await evaluate('document.querySelector("#local-jobs-list li")===row&&row.querySelector("button")===button'))throw Error('status refresh replaced job row');
 if(!await evaluate('Array.from(document.querySelectorAll(".job-detail-dialog dd")).some(n=>n.textContent==="Failed")'))throw Error('open card status remained stale');
 if(!await evaluate('document.querySelector("#local-jobs-status").textContent.startsWith("0 active")'))throw Error('terminal counted as active');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
