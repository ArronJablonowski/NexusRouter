package sessions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darwinrouter/providers"
	"darwinrouter/runtime"
)

type reader []runtime.Event

func (r reader) Read(_ context.Context, _ string, after int64, limit int) ([]runtime.Event, error) {
	if int(after) >= len(r) {
		return nil, nil
	}
	end := int(after) + 2
	if end > len(r) {
		end = len(r)
	}
	return r[int(after):end], nil
}
func fixture() reader {
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TaskCompleted}
	events := reader{}
	for i, k := range kinds {
		e := runtime.Event{Version: 1, ID: string(k), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Unix(100, 0), Kind: k, TurnID: "turn", AttemptID: "attempt"}
		events = append(events, e)
	}
	events[0].Data.Messages = []providers.Message{{Role: "user", Content: "hello"}}
	events[2].Data.Text = "ignored partial"
	events[3].Data.Text = "call tool"
	events[3].Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}
	for _, i := range []int{4, 5} {
		events[i].Data = runtime.Data{ToolCallID: "call", ToolName: "lookup", Effect: runtime.NoEffect}
	}
	events[5].Data.Text = "found"
	return events
}
func TestReplayAndUnfinishedEffects(t *testing.T) {
	events := fixture()
	s, err := Replay(context.Background(), events, "task")
	if err != nil || s.State != "completed" || len(s.Messages) != 3 || s.Messages[1].Content != "call tool" || s.Messages[2].ToolCallID != "call" {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = Replay(context.Background(), events[:5], "task")
	if err != nil || !s.UncertainEffects || !s.Pending["call"].Dispatched {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = Replay(context.Background(), events[:3], "task")
	if err != nil || !s.InterruptedTurn || len(s.Messages) != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}
func TestReplayRejectsCorruption(t *testing.T) {
	for _, name := range []string{"gap", "session", "pair", "attempt", "initial_orphan", "duplicate_dispatch", "premature_completion"} {
		t.Run(name, func(t *testing.T) {
			r := fixture()
			switch name {
			case "attempt":
				r[5].AttemptID = "other-attempt"
			case "initial_orphan":
				r[0].Data.Messages = []providers.Message{{Role: "tool", ToolCallID: "orphan"}}
			case "gap":
				r[2].Sequence++
			case "session":
				r[2].SessionID = "other"
			case "pair":
				r[5].Data.ToolCallID = "wrong"
			case "duplicate_dispatch":
				r[5].Kind = runtime.ToolStarted
			case "premature_completion":
				r[4].Kind = runtime.TaskCompleted
			}
			if _, err := Replay(context.Background(), r, "task"); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
func TestCompactionPreservesParallelToolBatch(t *testing.T) {
	m := []providers.Message{{Role: "user", Content: "old"}, {Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "a"}, {ID: "b"}}}, {Role: "tool", ToolCallID: "a"}, {Role: "tool", ToolCallID: "b"}, {Role: "assistant", Content: "answer"}}
	for _, keep := range []int{2, 3, 4} {
		out, err := Compact(m, keep, Summary{Decisions: []string{"retain decision"}})
		if err != nil || out.RemovedMessages != 1 || len(out.Recent) != 4 {
			t.Fatalf("%+v %v", out, err)
		}
	}
	out, err := Compact(m, 1, Summary{})
	if err != nil || out.RemovedMessages != 4 {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := Compact(m[:3], 1, Summary{}); err == nil {
		t.Fatal("unpaired batch compacted")
	}
}
