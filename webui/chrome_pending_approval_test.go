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

// A proposal can be persisted after tool.started and an initial successful
// empty read. It must appear without another lifecycle event or a page reload.
func TestChromeRunningChatDiscoversLateApproval(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/api/v1/session/csrf":
			writeChromeJSON(w, map[string]any{"version": 1, "csrf_token": "fixture"})
			return
		case "/app/api/v1/operations":
			w.Write([]byte(`{"version":1,"items":[],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats":
			w.Write([]byte(`{"version":1,"items":[{"version":1,"chat_id":"chat-fixture","state":"running","started_at":"2026-10-01T12:00:00Z"}],"has_more":false,"next_cursor":""}`))
			return
		case "/app/api/v1/chats/chat-fixture/messages":
			writeChromeJSON(w, HistoryPage{Version: 1, ChatID: "chat-fixture", TaskID: "task-fixture", HeadRevision: 1, Messages: []HistoryMessage{{ID: "message-fixture", Role: "user", Text: "Synthetic pending task", Revision: 1, SourceRevision: 1}}})
			return
		case "/app/api/v1/tasks/task-fixture/controls":
			w.Write([]byte(`{"version":1,"task_id":"task-fixture","revision":1,"can_resume":false,"can_steer":true,"can_cancel":true,"can_feedback":false}`))
			return
		case "/app/api/v1/tasks/task-fixture/feedback":
			writeChromeJSON(w, FeedbackContext{Version: 1, TaskID: "task-fixture", Revision: 1, Objective: []EvidenceSummary{}, FeedbackAllowed: false, DenialCode: "task_ineligible"})
			return
		case "/app/api/v1/tasks/task-fixture/approvals":
			page := ApprovalPage{Version: 1, TaskID: "task-fixture", Items: []ApprovalSummary{}}
			if reads.Add(1) > 1 {
				page.Items = []ApprovalSummary{{ID: "late-approval", State: "pending", Revision: 1, Prompt: "Allow synthetic tool", ScopeSummary: "Synthetic scope", ToolName: "read_file", ToolBehavior: "read_only", ExpiresAt: time.Now().Add(time.Minute), CanAllow: true, CanDeny: true}}
			}
			writeChromeJSON(w, page)
			return
		case "/app/api/v1/chats/chat-fixture/events":
			w.WriteHeader(204)
			return
		}
		if r.Method == http.MethodPost {
			t.Error("observation dispatched a mutation", r.URL.Path)
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/chats/chat-fixture'});
 await eventually('!!document.querySelector(".approval-message [data-approval-action=allow]")&&!document.querySelector(".approval-message [data-approval-action=allow]").hidden&&!document.querySelector(".approval-message [data-approval-action=allow]").disabled','Late pending proposal was never discovered');
 socket.close();`
	out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if reads.Load() < 2 {
		t.Fatal("fixture did not exercise successful empty read")
	}
}
