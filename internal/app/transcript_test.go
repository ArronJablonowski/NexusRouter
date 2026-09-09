package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func TestChatHistoryRedactsAndOmitsSystemAndToolContent(t *testing.T) {
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
		{Version: 1, ID: "tool-end", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 5, Time: now, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "read", Effect: runtime.NoEffect, Text: `{"value":"private tool ` + secret + `"}`}},
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
