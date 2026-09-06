package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type continuationReaderFunc func(context.Context, string, int64, int) ([]runtime.Event, error)

func (f continuationReaderFunc) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	return f(ctx, task, after, limit)
}

func recoveredModelHistory(t *testing.T, prefix []runtime.Event, canceled bool) []runtime.Event {
	t.Helper()
	plan, err := PlanInterruptedModel([][]runtime.Event{prefix}, prefix[0].Time.Add(time.Minute), canceled)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]runtime.Event(nil), prefix...), plan.Events...)
}

func TestReplayContinuationExactRecoveredModelBoundaries(t *testing.T) {
	for _, boundary := range []string{"start", "dispatch", "partial", "completed_turn", "historical_tools"} {
		t.Run(boundary, func(t *testing.T) {
			prefix := interruptedModelFixture()
			switch boundary {
			case "start":
				prefix = prefix[:1]
			case "dispatch":
				prefix = prefix[:2]
			case "completed_turn":
				prefix[2].Kind = runtime.TurnCompleted
				prefix[2].Data.Text = "completed answer"
			case "historical_tools":
				prefix[0].Data.Messages = []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "old-call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "old-call", Content: "saved result"}, {Role: "user", Content: "new question"}}
			}
			history := recoveredModelHistory(t, prefix, false)
			before, _ := json.Marshal(history)
			reads := 0
			r := continuationReaderFunc(func(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
				reads++
				return terminalReader(history).Read(ctx, task, after, limit)
			})
			snapshot, status, err := ReplayContinuation(context.Background(), r, "task")
			if err != nil || status.Validate() != nil || !status.HistoryEligible || status.Reason != "recovered_model" || status.State != "failed" || snapshot.State != "failed" || len(snapshot.Pending) != 0 || snapshot.UncertainEffects || reads != 2 {
				t.Fatal(snapshot, status, err, reads)
			}
			wantInterrupted := boundary != "start" && boundary != "completed_turn"
			if snapshot.InterruptedTurn != wantInterrupted {
				t.Fatal("raw interruption state rewritten")
			}
			for _, message := range snapshot.Messages {
				if strings.Contains(message.Content, "partial output") {
					t.Fatal("partial output imported")
				}
			}
			if boundary == "historical_tools" && (len(snapshot.Messages) != 3 || snapshot.Messages[1].Role != "tool" || snapshot.Messages[1].Content != "saved result") {
				t.Fatal("historical tool context lost")
			}
			if boundary == "completed_turn" && (len(snapshot.Messages) != 2 || snapshot.Messages[1].Content != "completed answer") {
				t.Fatal("completed model context lost")
			}
			snapshot.Messages[0].Content = "changed owned snapshot"
			after, _ := json.Marshal(history)
			if string(before) != string(after) {
				t.Fatal("snapshot aliases source reader")
			}
		})
	}
}

func TestReplayContinuationRejectsForgedOrOrdinaryModelFailure(t *testing.T) {
	for _, mode := range []string{"id", "cause", "time", "text", "normal_failure", "canceled", "pending", "current_tools", "readonly_child"} {
		t.Run(mode, func(t *testing.T) {
			history := recoveredModelHistory(t, interruptedModelFixture(), mode == "canceled")
			last := &history[len(history)-1]
			switch mode {
			case "id":
				last.ID = "forged"
			case "cause":
				last.CausationID = "wrong"
			case "time":
				last.Time = last.Time.Add(time.Nanosecond)
			case "text":
				last.Data.Text = "forged output"
			case "normal_failure":
				last.Data.Code = "execution_failed"
			case "readonly_child":
				last.Data.Code = "interrupted_read_only_model"
			case "pending", "current_tools":
				history = history[:3]
				history[2].Kind = runtime.TurnCompleted
				history[2].Data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call", Name: "read_file", Arguments: json.RawMessage(`{}`)}}}
				if mode == "current_tools" {
					e := history[2]
					e.Sequence = 4
					e.ID = "tool-start"
					e.Kind = runtime.ToolStarted
					e.Data = runtime.Data{ToolCallID: "call", ToolName: "read_file", ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.NoEffect}
					history = append(history, e)
					e.Sequence = 5
					e.ID = "tool-result"
					e.Kind = runtime.ToolCompleted
					e.Data.Text = `{"read":"value"}`
					history = append(history, e)
				}
				e := history[0]
				e.Sequence = int64(len(history) + 1)
				e.ID = "forged-terminal"
				e.Kind = runtime.TaskFailed
				e.Data = runtime.Data{Code: "interrupted_model"}
				e.TurnID, e.AttemptID = "turn-1", "attempt-1"
				history = append(history, e)
			}
			switch mode {
			case "id", "cause", "time", "text", "normal_failure":
				if _, err := Replay(context.Background(), terminalReader(history), "task"); err != nil {
					t.Fatal("forged proof fixture must remain replay-valid", err)
				}
			}
			_, status, err := ReplayContinuation(context.Background(), terminalReader(history), "task")
			if err == nil && status.HistoryEligible {
				t.Fatal("unqualified model failure became context", mode, status)
			}
		})
	}
}

func TestReplayContinuationExactAggregateLimits(t *testing.T) {
	for _, count := range []int{10000, 10001} {
		t.Run(fmt.Sprintf("events_%d", count), func(t *testing.T) {
			prefix := interruptedModelFixture()
			for len(prefix) < count-1 {
				e := prefix[2]
				e.Sequence = int64(len(prefix) + 1)
				e.ID = fmt.Sprintf("bounded-delta-%d", e.Sequence)
				prefix = append(prefix, e)
			}
			history := recoveredModelHistory(t, prefix, false)
			if len(history) != count {
				t.Fatal("incorrect count fixture")
			}
			if _, err := Replay(context.Background(), terminalReader(history), "task"); err != nil {
				t.Fatal("count fixture not replay-valid", err)
			}
			_, status, err := ReplayContinuation(context.Background(), terminalReader(history), "task")
			if count == 10000 {
				if err != nil || !status.HistoryEligible || status.Reason != "recovered_model" {
					t.Fatal("inclusive event bound rejected", status, err)
				}
			} else if err == nil || status.HistoryEligible {
				t.Fatal("terminal outside event bound accepted", status, err)
			}
		})
	}
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("bytes_plus_%d", extra), func(t *testing.T) {
			prefix := interruptedModelFixture()
			prefix[2].Data.Text = "x"
			for len(prefix) < 12 {
				e := prefix[2]
				e.Sequence = int64(len(prefix) + 1)
				e.ID = fmt.Sprintf("padded-delta-%d", e.Sequence)
				prefix = append(prefix, e)
			}
			history := recoveredModelHistory(t, prefix, false)
			total := 0
			for _, e := range history {
				body, err := e.Encode()
				if err != nil {
					t.Fatal(err)
				}
				total += len(body)
			}
			padding := (8 << 20) + extra - total
			for i := 2; i < len(history)-1 && padding > 0; i++ {
				add := min(padding, 900<<10)
				history[i].Data.Text += strings.Repeat("x", add)
				padding -= add
			}
			if padding != 0 {
				t.Fatal("fixture padding capacity insufficient")
			}
			total = 0
			for _, e := range history {
				body, err := e.Encode()
				if err != nil || len(body) >= 1<<20 {
					t.Fatal("individual event invalid or too large", len(body), err)
				}
				total += len(body)
			}
			if total != (8<<20)+extra {
				t.Fatal("fixture did not hit exact aggregate bound", total)
			}
			// Both histories have a valid prefix and exact recovery terminal; only
			// the complete encoded history crosses the bound in the second case.
			plan, err := PlanInterruptedModel([][]runtime.Event{history[:len(history)-1]}, history[len(history)-1].Time, false)
			if err != nil || !reflect.DeepEqual(plan.Events[0], history[len(history)-1]) {
				t.Fatal("padded recovery proof invalid", err)
			}
			_, status, err := ReplayContinuation(context.Background(), terminalReader(history), "task")
			if extra == 0 {
				if err != nil || !status.HistoryEligible {
					t.Fatal("inclusive byte bound rejected", status, err)
				}
			} else if err == nil || status.HistoryEligible {
				t.Fatal("terminal beyond aggregate byte limit accepted", status, err)
			}
		})
	}
}

func TestReplayContinuationCompletedUsesExistingAssessment(t *testing.T) {
	h := interruptedModelFixture()
	h[2].Kind = runtime.TurnCompleted
	h[2].Data.Text = "accepted completed message"
	e := h[2]
	e.ID = "terminal"
	e.Kind = runtime.TaskCompleted
	e.Sequence = 4
	e.Data = runtime.Data{}
	h = append(h, e)
	snapshot, status, err := ReplayContinuation(context.Background(), terminalReader(h), "task")
	if err != nil || !reflect.DeepEqual(status, AssessContinuation(snapshot, h[len(h)-2:])) || status.Reason != "completed" {
		t.Fatal(status, err)
	}
}

func TestReplayContinuationCancellationAndReaderBounds(t *testing.T) {
	for _, mode := range []string{"nil_context", "canceled", "reader_cancels", "reader_error", "oversized_page", "event_count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := recoveredModelHistory(t, interruptedModelFixture(), false)
			reads := 0
			if mode == "event_count" {
				h = interruptedModelFixture()
				for len(h) < 10001 {
					e := h[2]
					e.Sequence = int64(len(h) + 1)
					e.ID = fmt.Sprintf("delta-%d", e.Sequence)
					h = append(h, e)
				}
			}
			if mode == "bytes" {
				h[2].Data.Text = strings.Repeat("x", 8<<20)
			}
			r := continuationReaderFunc(func(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
				reads++
				if mode == "reader_error" {
					return nil, errors.New("fixture read error")
				}
				if mode == "reader_cancels" {
					cancel()
				}
				if mode == "oversized_page" {
					return make([]runtime.Event, 101), nil
				}
				return terminalReader(h).Read(ctx, task, after, limit)
			})
			if mode == "nil_context" {
				ctx = nil
			} else if mode == "canceled" {
				cancel()
			}
			snapshot, status, err := ReplayContinuation(ctx, r, "task")
			if err == nil || status.HistoryEligible || snapshot.TaskID != "" {
				t.Fatal("invalid read returned partial snapshot", mode, status, err)
			}
			if (mode == "nil_context" || mode == "canceled") && reads != 0 {
				t.Fatal("invalid context called reader")
			}
		})
	}
}
