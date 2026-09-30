package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func interruptedModelFixture() []runtime.Event {
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	return []runtime.Event{
		{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: at, Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "task prompt"}}}},
		{Version: 1, ID: "turn", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 2, Time: at, Kind: runtime.TurnStarted, TurnID: "turn-1", AttemptID: "attempt-1"},
		{Version: 1, ID: "delta", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 3, Time: at, Kind: runtime.ModelDelta, TurnID: "turn-1", AttemptID: "attempt-1", Data: runtime.Data{Text: "partial output must not be accepted"}},
	}
}

func TestPlanInterruptedModelTerminatesWithoutCompletingTurn(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			history := interruptedModelFixture()
			now := history[0].Time.Add(time.Minute)
			before, _ := json.Marshal(history)
			plan, err := PlanInterruptedModel([][]runtime.Event{history}, now, canceled)
			if err != nil || plan.ParentTaskID != "task" || plan.ExpectedSequence != 3 || len(plan.Events) != 1 {
				t.Fatalf("bad plan %+v %v", plan, err)
			}
			e := plan.Events[0]
			kind, code := runtime.TaskFailed, "interrupted_model"
			if canceled {
				kind, code = runtime.TaskCanceled, "canceled"
			}
			if e.Kind != kind || e.Data.Code != code || e.Sequence != 4 || e.TurnID != "turn-1" || e.AttemptID != "attempt-1" || e.CausationID != "turn" || e.Data.Text != "" || e.Time != now {
				t.Fatal("terminal attribution or content wrong")
			}
			second, err := PlanInterruptedModel([][]runtime.Event{history}, now, canceled)
			if err != nil || second.Events[0].ID != e.ID {
				t.Fatal("unstable event identity")
			}
			full := append(append([]runtime.Event(nil), history...), plan.Events...)
			snapshot, err := Replay(context.Background(), terminalReader(full), "task")
			if err != nil || !snapshot.InterruptedTurn || snapshot.UncertainEffects || len(snapshot.Pending) != 0 || len(snapshot.Messages) != 1 {
				t.Fatal("terminal replay invented completion", err)
			}
			status := AssessContinuation(snapshot, full)
			if status.HistoryEligible {
				t.Fatal("interrupted model history eligible")
			}
			after, _ := json.Marshal(history)
			if !bytes.Equal(before, after) {
				t.Fatal("caller mutated")
			}
			plan.Events[0].Data.Code = "changed"
			if history[2].Data.Code != "" || second.Events[0].Data.Code != code {
				t.Fatal("plan aliases source or another plan")
			}
		})
	}
}

func TestPlanInterruptedModelAllowsHistoricalPairsAndClosedTurns(t *testing.T) {
	h := interruptedModelFixture()
	h[0].Data.ParentTaskID = "historical-parent"
	h[0].Data.Messages = []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "old-call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "old-call", Content: "old result"}, {Role: "user", Content: "continue"}}
	completed := h[2]
	completed.Kind = runtime.TurnCompleted
	completed.Data = runtime.Data{Text: "completed previous turn"}
	turn := h[1]
	turn.Sequence = 4
	turn.ID = "next-turn"
	turn.TurnID = "turn-2"
	turn.AttemptID = "attempt-2"
	h = []runtime.Event{h[0], h[1], completed, turn}
	plan, err := PlanInterruptedModel([][]runtime.Event{h}, h[0].Time.Add(time.Minute), false)
	if err != nil || plan.Events[0].CausationID != "next-turn" || plan.Events[0].TurnID != "turn-2" {
		t.Fatal("historical context or model-only earlier turn refused", err)
	}
}

func TestPlanInterruptedModelRefusesAmbiguousHistories(t *testing.T) {
	for name, mutate := range map[string]func([]runtime.Event) [][]runtime.Event{
		"none":     func(h []runtime.Event) [][]runtime.Event { return nil },
		"children": func(h []runtime.Event) [][]runtime.Event { return [][]runtime.Event{h, h} },
		"worker":   func(h []runtime.Event) [][]runtime.Event { h[0].WorkerID = "worker"; return [][]runtime.Event{h} },
		"retry": func(h []runtime.Event) [][]runtime.Event {
			h[0].Data.RetryOfTaskID = "prior"
			return [][]runtime.Event{h}
		},
		"proposal": func(h []runtime.Event) [][]runtime.Event {
			h[2].Kind = runtime.TurnCompleted
			h[2].Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{}`)}}
			return [][]runtime.Event{h}
		},
		"tool data": func(h []runtime.Event) [][]runtime.Event {
			h[2].Data.Effect = runtime.UncertainEffect
			return [][]runtime.Event{h}
		},
		"approval error": func(h []runtime.Event) [][]runtime.Event {
			h[2].Kind = runtime.ErrorRecorded
			h[2].Data.Code = "approval_required"
			return [][]runtime.Event{h}
		},
		"evaluation": func(h []runtime.Event) [][]runtime.Event {
			yes := true
			h[2].Kind = runtime.EvaluationRecorded
			h[2].Data.Accepted = &yes
			return [][]runtime.Event{h}
		},
		"terminal":      func(h []runtime.Event) [][]runtime.Event { h[2].Kind = runtime.TaskFailed; return [][]runtime.Event{h} },
		"wrong attempt": func(h []runtime.Event) [][]runtime.Event { h[2].AttemptID = "other"; return [][]runtime.Event{h} },
		"invalid ID":    func(h []runtime.Event) [][]runtime.Event { h[0].ID = "bad\nID"; return [][]runtime.Event{h} },
		"future": func(h []runtime.Event) [][]runtime.Event {
			h[2].Time = h[2].Time.Add(time.Hour)
			return [][]runtime.Event{h}
		},
		"oversized": func(h []runtime.Event) [][]runtime.Event {
			h[2].Data.Text = strings.Repeat("x", 8<<20)
			return [][]runtime.Event{h}
		},
		"too many": func(h []runtime.Event) [][]runtime.Event {
			for len(h) < 10001 {
				e := h[2]
				e.ID = fmt.Sprint("e", len(h))
				e.Sequence = int64(len(h) + 1)
				h = append(h, e)
			}
			return [][]runtime.Event{h}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := interruptedModelFixture()
			plan, err := PlanInterruptedModel(mutate(h), h[0].Time.Add(time.Minute), false)
			if err == nil || plan.ParentTaskID != "" || len(plan.Events) != 0 {
				t.Fatal("ambiguous history admitted")
			}
		})
	}
}

func TestPlanInterruptedModelBeforeAndAfterModelBoundaries(t *testing.T) {
	for _, stage := range []string{"task_started", "turn_started", "turn_completed"} {
		t.Run(stage, func(t *testing.T) {
			h := interruptedModelFixture()
			switch stage {
			case "task_started":
				h = h[:1]
			case "turn_started":
				h = h[:2]
			case "turn_completed":
				h[2].Kind = runtime.TurnCompleted
			}
			plan, err := PlanInterruptedModel([][]runtime.Event{h}, h[0].Time.Add(time.Minute), false)
			if err != nil || len(plan.Events) != 1 || plan.Events[0].Kind != runtime.TaskFailed || plan.Events[0].Data.Text != "" {
				t.Fatal("safe boundary not failed", err)
			}
			terminal := plan.Events[0]
			if stage == "task_started" {
				if terminal.TurnID != "" || terminal.AttemptID != "" || terminal.CausationID != "start" {
					t.Fatal("pre-turn identity invented")
				}
			} else if terminal.TurnID != "turn-1" || terminal.AttemptID != "attempt-1" || terminal.CausationID != "turn" {
				t.Fatal("latest turn attribution lost")
			}
			full := append(append([]runtime.Event(nil), h...), terminal)
			snapshot, err := Replay(context.Background(), terminalReader(full), "task")
			if err != nil || snapshot.State != "failed" || AssessContinuation(snapshot, full).HistoryEligible {
				t.Fatal("failure made history eligible", err)
			}
		})
	}
}

func TestPlanInterruptedModelRejectsPriorCompletedToolExecution(t *testing.T) {
	h := interruptedDelegationFixture(t)[0]
	completed := h[3]
	completed.Kind, completed.ID, completed.Sequence = runtime.ToolCompleted, "tool-result", 5
	completed.Data.Effect = runtime.NoEffect
	completed.Data.Text = "finished tool"
	next := h[1]
	next.ID, next.TurnID, next.AttemptID, next.Sequence = "new-turn", "new-turn", "new-attempt", 6
	h = append(h, completed, next)
	if snapshot, err := Replay(context.Background(), terminalReader(h), h[0].TaskID); err != nil || !snapshot.InterruptedTurn || len(snapshot.Pending) != 0 || snapshot.UncertainEffects {
		t.Fatal("fixture should be replay-valid tool history", err)
	}
	if plan, err := PlanInterruptedModel([][]runtime.Event{h}, h[0].Time.Add(time.Hour), false); err == nil || len(plan.Events) != 0 {
		t.Fatal("prior current-journal tool execution admitted")
	}
}
