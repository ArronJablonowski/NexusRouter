package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestRedactingJournalPreservesCanonicalToolArgumentNumberPrecision(t *testing.T) {
	ctx := context.Background()
	store := openRuntimeHostStore(t, "redacting-number.db")
	journal := redactingJournal{db: store}
	now := time.Now().UTC()
	events := []runtime.Event{
		{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: now, Kind: runtime.TaskStarted},
		{Version: 1, ID: "turn-start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ModelID: "model", ProviderID: "provider"}},
		{Version: 1, ID: "turn-done", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 3, Time: now.Add(2 * time.Millisecond), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{"a":true,"z":{"a":1,"n":9007199254740993}}`)}}}},
	}
	for i, event := range events {
		if err := journal.Append(ctx, int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := store.Read(ctx, "task", 0, 10)
	if err != nil || len(stored) != 3 {
		t.Fatal(stored, err)
	}
	want := `{"a":true,"z":{"a":1,"n":9007199254740993}}`
	if got := string(stored[2].Data.ToolCalls[0].Arguments); got != want {
		t.Fatalf("stored arguments = %q, want %q", got, want)
	}
}
