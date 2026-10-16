package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func recordChatFileTool(t *testing.T, s *Service, task, chat, name, args, result string, effect runtime.Effect) {
	t.Helper()
	db, e := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	data := []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "Create a file"}}}},
		{Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"},
		{Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call", Name: name, Arguments: json.RawMessage(args)}}}},
		{Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: name, ToolBehavior: runtime.BehaviorNonIdempotentWrite, Effect: runtime.NoEffect}},
		{Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: name, ToolBehavior: runtime.BehaviorNonIdempotentWrite, Effect: effect, Text: result}},
		{Kind: runtime.TurnStarted, TurnID: "finish", AttemptID: "finish-attempt"},
		{Kind: runtime.TurnCompleted, TurnID: "finish", AttemptID: "finish-attempt", Data: runtime.Data{Text: "Your file is ready. [private](file:///etc/passwd)"}},
		{Kind: runtime.TaskCompleted},
	}
	if effect == runtime.UncertainEffect {
		data = data[:5]
		data = append(data, runtime.Event{Kind: runtime.TaskFailed, Data: runtime.Data{Code: "tool_uncertain"}})
	}
	for i, event := range data {
		event.Version = 1
		event.ID = task + "-" + string(rune('a'+i))
		event.TaskID = task
		event.SessionID = chat
		event.CorrelationID = task
		event.Sequence = int64(i + 1)
		event.Time = time.Unix(100, 0).UTC()
		if e = db.Append(context.Background(), int64(i), event); e != nil {
			t.Fatal(e)
		}
	}
}
func TestChatFilesRequireConfirmedCreationAndChatOwnership(t *testing.T) {
	ctx := context.Background()
	s := submissionService(t)
	recordChatFileTool(t, s, "made", "chat", "create_file", `{"path":"reports/report.md","content":"# Hello\nReviewed output"}`, `{"created":true}`, runtime.ConfirmedEffect)
	recordChatFileTool(t, s, "uncertain", "chat", "create_file", `{"path":"uncertain.md","content":"must not expose"}`, `{"created":true}`, runtime.UncertainEffect)
	recordChatFileTool(t, s, "read", "chat", "read", `{"path":"private.txt"}`, `{"created":true}`, runtime.NoEffect)
	recordChatFileTool(t, s, "other", "other-chat", "create_file", `{"path":"other.txt","content":"another chat"}`, `{"created":true}`, runtime.ConfirmedEffect)
	page, e := s.ChatFiles(ctx, "chat")
	if e != nil || page.Validate() != nil || len(page.Files) != 1 {
		t.Fatal(page, e)
	}
	f, b, e := s.ChatFile(ctx, "chat", page.Files[0].ID)
	if e != nil || f.Name != "report.md" || string(b) != "# Hello\nReviewed output" {
		t.Fatal(f, string(b), e)
	}
	if _, _, e = s.ChatFile(ctx, "other-chat", f.ID); e == nil {
		t.Fatal("cross-chat download")
	}
	if _, e = s.ChatFiles(ctx, "missing-chat"); e == nil {
		t.Fatal("unknown chat")
	}
	again, e := s.ChatFiles(ctx, "chat")
	if e != nil || len(again.Files) != 1 || again.Files[0].ID != f.ID {
		t.Fatal("reconcile duplicated files", again, e)
	}
}

func TestChatFilesReplacementExportsOnlyNewReviewedContent(t *testing.T) {
	s := submissionService(t)
	recordChatFileTool(t, s, "replace-task", "replace-chat", "replace_file", `{"path":"notes.txt","expected_content":"old private preimage","content":"new reviewed version"}`, `{"replaced":true,"backup":"private-recovery-location"}`, runtime.ConfirmedEffect)
	page, e := s.ChatFiles(context.Background(), "replace-chat")
	if e != nil || len(page.Files) != 1 {
		t.Fatal(page, e)
	}
	_, b, e := s.ChatFile(context.Background(), "replace-chat", page.Files[0].ID)
	if e != nil || string(b) != "new reviewed version" {
		t.Fatal("replacement snapshot", e, string(b))
	}
}
