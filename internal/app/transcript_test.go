package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestChatHistoryRedactsAndOmitsSystemAndToolContent(t *testing.T) {
	for _, output := range []string{`{"value":"private tool browser-private-credential"}`, "[project]\nname = 'private tool browser-private-credential'", "{\"ok\":true}\ncommand completed", "[truncated output"} {
		t.Run(strconv.Itoa(len(output)), func(t *testing.T) { checkChatHistoryHiddenToolOutput(t, output) })
	}
}

func checkChatHistoryHiddenToolOutput(t *testing.T, toolOutput string) {
	service := submissionService(t)
	const secret = "browser-private-credential"
	service.secret = func(string) string { return secret }
	store, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(100, 0).UTC()
	events := []runtime.Event{
		{Version: 1, ID: "start", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "system", Content: "private system " + secret}, {Role: "user", Content: "use " + secret}}}},
		{Version: 1, ID: "turn", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 2, Time: now, Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"},
		{Version: 1, ID: "answer", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 3, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{"token":"` + secret + `"}`)}}}},
		{Version: 1, ID: "tool-start", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 4, Time: now, Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "read", Effect: runtime.NoEffect}},
		{Version: 1, ID: "tool-end", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 5, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "read", Effect: runtime.NoEffect, Text: toolOutput}},
		{Version: 1, ID: "turn-2", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 6, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-2", AttemptID: "attempt-2"},
		{Version: 1, ID: "answer-2", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 7, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{Text: "safe answer " + secret}},
		{Version: 1, ID: "done", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 8, Time: now, Kind: runtime.TaskCompleted},
	}
	for index, event := range events {
		if err := store.Append(context.Background(), int64(index), event); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1})
	body, _ := json.Marshal(first)
	if err != nil || first.Validate() != nil || strings.Contains(string(body), secret) || strings.Contains(string(body), "private system") || strings.Contains(string(body), "private tool") || strings.Contains(string(body), "tool_calls") || len(first.Messages) != 1 || first.Messages[0].Role != "user" || !first.HasMore {
		t.Fatal("unsafe history projection", string(body), err)
	}
	second, err := service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1, After: first.NextCursor})
	if err != nil || second.Validate() != nil || len(second.Messages) != 1 || second.Messages[0].Role != "assistant" || second.Messages[0].Revision != 2 || second.Messages[0].SourceRevision != 7 {
		t.Fatal("history pagination lost source revision", second, err)
	}
	foreign, _ := decodeHistoryCursor(first.NextCursor)
	foreign.ChatID = "other"
	foreignCursor, _ := encodeHistoryCursor(foreign)
	if _, err = service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1, After: foreignCursor}); !errors.Is(err, ErrAdmission) {
		t.Fatal("foreign history cursor accepted", err)
	}
	foreign.ChatID, foreign.Head = "chat", 99
	aheadCursor, _ := encodeHistoryCursor(foreign)
	if _, err = service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1, After: aheadCursor}); !errors.Is(err, ErrAdmission) {
		t.Fatal("ahead history cursor accepted", err)
	}
}

func TestChatHistorySkipsNewerDelegationTasksAndPaginatesIdempotently(t *testing.T) {
	service := submissionService(t)
	store, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	parent := []runtime.Event{
		{Version: 1, ID: "parent-start", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "inspect the desktop"}}}},
		{Version: 1, ID: "parent-turn-1", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 2, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-1", AttemptID: "attempt-1"},
		{Version: 1, ID: "parent-call-1", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 3, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn-1", AttemptID: "attempt-1", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "delegate", Arguments: json.RawMessage(`{"task":"inspect"}`)}}}},
		{Version: 1, ID: "parent-tool-start-1", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 4, Time: now, Kind: runtime.ToolStarted, TurnID: "turn-1", AttemptID: "attempt-1", Data: runtime.Data{ToolCallID: "call-1", ToolName: "delegate", Effect: runtime.NoEffect}},
		{Version: 1, ID: "parent-tool-end-1", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 5, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn-1", AttemptID: "attempt-1", Data: runtime.Data{ToolCallID: "call-1", ToolName: "delegate", Effect: runtime.NoEffect, Text: `{"work_task_id":"work-1","untrusted_output":"first"}`}},
		{Version: 1, ID: "parent-turn-2", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 6, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-2", AttemptID: "attempt-2"},
		{Version: 1, ID: "parent-call-2", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 7, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call-2", Name: "delegate", Arguments: json.RawMessage(`{"task":"verify"}`)}}}},
		{Version: 1, ID: "parent-tool-start-2", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 8, Time: now, Kind: runtime.ToolStarted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{ToolCallID: "call-2", ToolName: "delegate", Effect: runtime.NoEffect}},
		{Version: 1, ID: "parent-tool-end-2", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 9, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{ToolCallID: "call-2", ToolName: "delegate", Effect: runtime.NoEffect, Text: `{"work_task_id":"work-2","untrusted_output":"second"}`}},
		{Version: 1, ID: "parent-turn-3", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 10, Time: now, Kind: runtime.TurnStarted, TurnID: "turn-3", AttemptID: "attempt-3"},
		{Version: 1, ID: "parent-answer", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 11, Time: now, Kind: runtime.TurnCompleted, TurnID: "turn-3", AttemptID: "attempt-3", Data: runtime.Data{Text: "inspection complete"}},
		{Version: 1, ID: "parent-done", TaskID: "parent", SessionID: "chat", CorrelationID: "parent", Sequence: 12, Time: now, Kind: runtime.TaskCompleted},
	}
	for index, event := range parent {
		if err := store.Append(context.Background(), int64(index), event); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"work-1", "work-2"} {
		start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: "chat", CorrelationID: task, Sequence: 1, Time: now.Add(time.Second), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: "parent"}}
		done := runtime.Event{Version: 1, ID: task + "-done", TaskID: task, SessionID: "chat", CorrelationID: task, Sequence: 2, Time: now.Add(2 * time.Second), Kind: runtime.TaskCompleted}
		if err := store.Append(context.Background(), 0, start); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(context.Background(), 1, done); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1})
	if err != nil || first.TaskID != "parent" || first.HeadRevision != 12 || len(first.Messages) != 1 || first.Messages[0].Role != "user" || first.Messages[0].SourceRevision != 1 || !first.HasMore {
		t.Fatal("delegated work task displaced parent transcript", first, err)
	}
	retry, err := service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1})
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("history retry was not idempotent", retry, err)
	}
	second, err := service.ChatHistory(context.Background(), "chat", contract.HistoryOptions{Limit: 1, After: first.NextCursor})
	if err != nil || second.TaskID != "parent" || len(second.Messages) != 1 || second.Messages[0].Role != "assistant" || second.Messages[0].Text != "inspection complete" || second.Messages[0].Revision != 2 || second.Messages[0].SourceRevision != 11 || second.HasMore {
		t.Fatal("sparse delegated transcript pagination was incomplete", second, err)
	}
}
