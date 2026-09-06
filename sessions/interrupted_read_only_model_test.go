package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func interruptedReadOnlyFixture() []runtime.Event {
	h := interruptedModelFixture()[:2]
	h[0].Data.ParentTaskID = "worker-task"
	proposal := h[1]
	proposal.ID = "proposal"
	proposal.Sequence = 3
	proposal.Kind = runtime.TurnCompleted
	proposal.Data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "read-call", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}}
	started := proposal
	started.ID = "tool-start"
	started.Sequence = 4
	started.Kind = runtime.ToolStarted
	started.Data = runtime.Data{ToolCallID: "read-call", ToolName: "read_file", ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect}
	completed := started
	completed.ID = "tool-end"
	completed.Sequence = 5
	completed.Kind = runtime.ToolCompleted
	completed.Data.Effect = runtime.NoEffect
	completed.Data.Text = "untrusted source text"
	next := h[1]
	next.ID = "next-turn"
	next.Sequence = 6
	next.TurnID = "next-turn"
	next.AttemptID = "next-attempt"
	delta := next
	delta.ID = "next-delta"
	delta.Sequence = 7
	delta.Kind = runtime.ModelDelta
	delta.Data.Text = "partial code"
	return append(h, proposal, started, completed, next, delta)
}

func TestPlanInterruptedReadOnlyModelMultipleTools(t *testing.T) {
	h := interruptedReadOnlyFixture()[:5]
	h[2].Data.ToolCalls = append(h[2].Data.ToolCalls, providers.ToolCall{ID: "second-read", Name: "read_file", Arguments: json.RawMessage(`{}`)})
	now := h[0].Time.Add(time.Minute)
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err == nil {
		t.Fatal("unresolved second proposal admitted")
	}
	start, end := h[3], h[4]
	start.ID, start.Sequence, start.Data.ToolCallID = "second-start", 6, "second-read"
	end.ID, end.Sequence, end.Data.ToolCallID = "second-end", 7, "second-read"
	h = append(h, start)
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err == nil {
		t.Fatal("unfinished second dispatch admitted")
	}
	h = append(h, end)
	if plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err != nil || len(plan.Events) != 1 {
		t.Fatal("resolved multiple reads rejected", err)
	}
}

func TestPlanInterruptedReadOnlyModelFullHistoryLimits(t *testing.T) {
	h := interruptedReadOnlyFixture()
	now := h[0].Time.Add(time.Minute)
	for len(h) < 9999 {
		e := h[6]
		e.ID, e.Sequence = fmt.Sprint("delta-", len(h)), int64(len(h)+1)
		h = append(h, e)
	}
	if plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err != nil || plan.Events[0].Sequence != 10000 {
		t.Fatal("exact full event limit rejected", err)
	}
	e := h[6]
	e.ID, e.Sequence = "last-delta", 10000
	h = append(h, e)
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err == nil {
		t.Fatal("terminal event not counted")
	}
	h = interruptedReadOnlyFixture()
	plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range h {
		body, _ := e.Encode()
		total += len(body)
	}
	terminal, _ := plan.Events[0].Encode()
	h[4].Data.Text += strings.Repeat("x", (8<<20)-total-len(terminal))
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err != nil {
		t.Fatal("exact full byte limit rejected", err)
	}
	h[4].Data.Text += "x"
	if _, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now); err == nil {
		t.Fatal("terminal bytes not counted")
	}
}

func TestPlanInterruptedReadOnlyModelPreservesHistory(t *testing.T) {
	for _, length := range []int{5, 6, 7} {
		t.Run(string(rune('0'+length)), func(t *testing.T) {
			h := interruptedReadOnlyFixture()[:length]
			before, _ := json.Marshal(h)
			now := h[0].Time.Add(time.Minute)
			plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now)
			if err != nil || len(plan.Events) != 1 || plan.Events[0].Data.Code != "interrupted_read_only_model" || plan.Events[0].Kind != runtime.TaskFailed || plan.Events[0].Data.Text != "" || plan.ExpectedSequence != int64(length) {
				t.Fatal(plan, err)
			}
			again, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, now)
			if err != nil || !reflect.DeepEqual(plan, again) {
				t.Fatal("nondeterministic")
			}
			after, _ := json.Marshal(h)
			if !bytes.Equal(before, after) {
				t.Fatal("history modified")
			}
			full := append(append([]runtime.Event(nil), h...), plan.Events...)
			snap, err := Replay(context.Background(), terminalReader(full), h[0].TaskID)
			if err != nil || snap.State != "failed" || len(snap.Pending) != 0 || snap.UncertainEffects || !isInterruptedModelTerminal(full) {
				t.Fatal(snap, err)
			}
			if old, err := PlanInterruptedModel([][]runtime.Event{h}, now, false); err == nil || len(old.Events) != 0 {
				t.Fatal("old semantics broadened")
			}
		})
	}
}

func TestPlanInterruptedReadOnlyModelRejectsUnsafeHistory(t *testing.T) {
	for name, mutate := range map[string]func([]runtime.Event) []runtime.Event{
		"legacy-start": func(h []runtime.Event) []runtime.Event { h[3].Data.ToolBehavior = ""; return h },
		"legacy-both": func(h []runtime.Event) []runtime.Event {
			h[3].Data.ToolBehavior = ""
			h[4].Data.ToolBehavior = ""
			return h
		},
		"write": func(h []runtime.Event) []runtime.Event {
			h[3].Data.ToolBehavior = runtime.BehaviorNonIdempotentWrite
			h[4].Data.ToolBehavior = runtime.BehaviorNonIdempotentWrite
			return h
		},
		"confirmed": func(h []runtime.Event) []runtime.Event { h[4].Data.Effect = runtime.ConfirmedEffect; return h },
		"uncertain": func(h []runtime.Event) []runtime.Event { h[4].Data.Effect = runtime.UncertainEffect; return h },
		"pending":   func(h []runtime.Event) []runtime.Event { return h[:4] },
		"unstarted": func(h []runtime.Event) []runtime.Event { return h[:3] },
		"not-child": func(h []runtime.Event) []runtime.Event { h[0].Data.ParentTaskID = ""; return h },
		"retry":     func(h []runtime.Event) []runtime.Event { h[0].Data.RetryOfTaskID = "previous"; return h },
		"delegate": func(h []runtime.Event) []runtime.Event {
			h[2].Data.ToolCalls[0].Name = "delegate"
			h[3].Data.ToolName = "delegate"
			h[4].Data.ToolName = "delegate"
			return h
		},
		"error": func(h []runtime.Event) []runtime.Event {
			h[6].Kind = runtime.ErrorRecorded
			h[6].Data.Code = "provider_error"
			return h
		},
		"evaluation": func(h []runtime.Event) []runtime.Event {
			v := true
			h[6].Kind = runtime.EvaluationRecorded
			h[6].Data.Accepted = &v
			return h
		},
		"terminal":   func(h []runtime.Event) []runtime.Event { h[6].Kind = runtime.TaskFailed; return h },
		"mismatched": func(h []runtime.Event) []runtime.Event { h[4].Data.ToolCallID = "different"; return h },
		"budget":     func(h []runtime.Event) []runtime.Event { h[4].Data.Text = strings.Repeat("x", 8<<20); return h },
	} {
		t.Run(name, func(t *testing.T) {
			h := mutate(interruptedReadOnlyFixture())
			plan, err := PlanInterruptedReadOnlyModel([][]runtime.Event{h}, h[0].Time.Add(time.Minute))
			if err == nil || !reflect.DeepEqual(plan, InterruptionRecovery{}) {
				t.Fatal(plan, err)
			}
		})
	}
	for _, h := range [][][]runtime.Event{nil, {interruptedModelFixture()}, {interruptedReadOnlyFixture(), interruptedReadOnlyFixture()}} {
		if plan, err := PlanInterruptedReadOnlyModel(h, time.Now().UTC()); err == nil || len(plan.Events) != 0 {
			t.Fatal("invalid journal count/tool count")
		}
	}
}

func TestPlanInterruptedReadOnlyWorkerTreeExactTerminal(t *testing.T) {
	h := runningWorkerTree(t)
	child := interruptedReadOnlyFixture()
	for i := range child {
		child[i].TaskID = h[1][0].TaskID
		child[i].SessionID = h[1][0].SessionID
		child[i].CorrelationID = h[1][0].TaskID
		child[i].Time = h[1][0].Time
		if child[i].Kind == runtime.TaskStarted || child[i].Kind == runtime.TurnStarted {
			child[i].Data.ModelID = h[1][0].Data.ModelID
			child[i].Data.ProviderID = h[1][0].Data.ProviderID
		}
	}
	child[0].Data.ParentTaskID = h[0][0].TaskID
	child[0].Data.SubmissionID = h[1][0].Data.SubmissionID
	h[1] = child
	now := child[0].Time.Add(time.Minute)
	plan, err := PlanInterruptedWorkerTree(h, now)
	if err != nil || plan.Child == nil {
		t.Fatal(plan, err)
	}
	child = append(append([]runtime.Event(nil), child...), plan.Child.Events...)
	work := append(append([]runtime.Event(nil), h[0]...), plan.Worker.Events...)
	payload, err := recoveryWorkResult(work, child)
	if err != nil || bytes.Contains(payload, []byte("untrusted_output")) || !bytes.Contains(payload, []byte("interrupted_read_only_model")) {
		t.Fatal(string(payload), err)
	}
	for _, mode := range []string{"id", "code", "text", "causation"} {
		t.Run(mode, func(t *testing.T) {
			bad := append([]runtime.Event(nil), child...)
			end := &bad[len(bad)-1]
			switch mode {
			case "id":
				end.ID = "forged"
			case "code":
				end.Data.Code = "interrupted_model"
			case "text":
				end.Data.Text = "accepted output"
			case "causation":
				end.CausationID = "forged"
			}
			if isInterruptedModelTerminal(bad) {
				t.Fatal("forged proof accepted")
			}
			if _, err := PlanInterruptedWorker([][]runtime.Event{h[0], bad}, now); err == nil {
				t.Fatal("forged worker child accepted")
			}
			if _, err := recoveryWorkResult(work, bad); err == nil {
				t.Fatal("forged payload accepted")
			}
		})
	}
}
