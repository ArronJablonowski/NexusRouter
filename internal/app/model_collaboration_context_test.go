package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
)

func TestModelCollaborationCompactReceiptAndLosslessPaging(t *testing.T) {
	ctx := context.Background()
	s, _ := autoFixture(t)
	store, err := agentchat.Open(ctx, t.TempDir()+"/journal")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sender, _ := collaborationSender(s.settings.Models[0], s.settings.Providers[0], Request{}, "task", true)
	sender.SessionID = "session"
	e := &modelCollaborationExecutor{store: store, sender: sender, models: s.settings.Models, taskID: "task", sessionID: "session"}
	call := func(id, name, args string) runtime.ToolResult {
		t.Helper()
		r, err := e.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}})
		if err != nil || r.Failed {
			t.Fatal(r, err)
		}
		return r
	}
	for i := 0; i < 12; i++ {
		args, _ := json.Marshal(map[string]string{"recipient": "*", "topic": "paging", "text": strings.Repeat("idea ", 100)})
		r := call(fmt.Sprint(i), "collaboration_send", string(args))
		if len(r.Content) > 64 || strings.Contains(r.Content, "idea") || r.Effect != runtime.ConfirmedEffect {
			t.Fatal("receipt echoes context", r)
		}
		// Replaying the same scoped call observes the original durable append.
		again := call(fmt.Sprint(i), "collaboration_send", string(args))
		if again.Content != r.Content {
			t.Fatal("receipt replay changed")
		}
	}
	var page webui.CollaborationPage
	json.Unmarshal([]byte(call("read", "collaboration_read", `{"topic":"paging"}`).Content), &page)
	if len(page.Messages) != 10 || page.NextBefore == 0 {
		t.Fatal("default page not bounded", page)
	}
	seen := map[int64]bool{}
	before := int64(0)
	for {
		r := call(fmt.Sprint("read-", before), "collaboration_read", fmt.Sprintf(`{"topic":"paging","limit":3,"before":%d}`, before))
		if err := json.Unmarshal([]byte(r.Content), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) > 3 {
			t.Fatal("limit ignored")
		}
		for _, m := range page.Messages {
			if seen[m.Sequence] {
				t.Fatal("duplicate page")
			}
			seen[m.Sequence] = true
			if m.SenderModel != sender.SenderModel || m.Hostname != sender.Hostname {
				t.Fatal("provenance lost")
			}
		}
		if page.NextBefore == 0 {
			break
		}
		before = page.NextBefore
	}
	if len(seen) != 12 {
		t.Fatal("messages skipped", len(seen))
	}
	for _, args := range []string{`{"limit":0}`, `{"limit":51}`, `{"limit":-1}`, `{"limit":1.5}`, `{"limit":1,"limit":2}`} {
		_, err := e.ExecuteScoped(ctx, runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: "invalid", Name: "collaboration_read", Arguments: json.RawMessage(args)}})
		if err == nil {
			t.Fatal("invalid limit accepted", args)
		}
	}
}
