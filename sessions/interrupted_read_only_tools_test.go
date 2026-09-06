package sessions

import (
	"bytes"
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

func pendingReadOnlyFixture(count int) []runtime.Event {
	h := interruptedReadOnlyFixture()[:4]
	h[2].Data.ToolCalls = nil
	for i := 0; i < count; i++ {
		h[2].Data.ToolCalls = append(h[2].Data.ToolCalls, providers.ToolCall{ID: fmt.Sprintf("call-%d", i), Name: "read_file", Arguments: json.RawMessage(`{}`)})
	}
	start := h[3]
	h = h[:3]
	// Dispatch in reverse proposal order, proving ordering follows dispatches.
	for i := count - 1; i >= 0; i-- {
		e := start
		e.ID = fmt.Sprintf("start-%d", i)
		e.Sequence = int64(len(h) + 1)
		e.Data.ToolCallID = fmt.Sprintf("call-%d", i)
		h = append(h, e)
	}
	return h
}

func TestPlanInterruptedReadOnlyToolsFailedPairsAndDeterminism(t *testing.T) {
	for _, count := range []int{1, 2, 32} {
		h := pendingReadOnlyFixture(count)
		now := h[0].Time.Add(time.Minute)
		before, _ := json.Marshal(h)
		plan, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now)
		if err != nil || plan.ExpectedSequence != int64(len(h)) || len(plan.Events) != count+1 {
			t.Fatal(plan, err)
		}
		for i, e := range plan.Events[:count] {
			if e.Kind != runtime.ToolCompleted || e.Data.ToolCallID != h[3+i].Data.ToolCallID || e.Data.Code != "tool_failed" || e.Data.Effect != runtime.NoEffect || e.Data.ToolBehavior != runtime.BehaviorReadOnly || e.Data.Text != `{"error":"read_only_tool_interrupted"}` {
				t.Fatal("not dispatch-ordered failure", e)
			}
		}
		end := plan.Events[count]
		if end.Data.Code != "interrupted_read_only_tool" || end.CausationID != h[len(h)-1].ID || end.Data.Text != "" {
			t.Fatal("unbound terminal", end)
		}
		again, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now)
		if err != nil || !reflect.DeepEqual(again, plan) {
			t.Fatal("nondeterministic", err)
		}
		full := append(append([]runtime.Event(nil), h...), plan.Events...)
		if !isInterruptedModelTerminal(full) {
			t.Fatal("exact suffix not recognized")
		}
		state, err := Replay(context.Background(), terminalReader(full), h[0].TaskID)
		if err != nil || state.State != "failed" || state.InterruptedTurn || state.UncertainEffects || len(state.Pending) != 0 {
			t.Fatal(state, err)
		}
		failed := 0
		for _, message := range state.Messages {
			if message.Role == "tool" {
				if !message.ToolFailed {
					t.Fatal("synthetic result treated as success")
				}
				failed++
			}
		}
		if failed != count {
			t.Fatal("missing failed pairs", failed)
		}
		after, _ := json.Marshal(h)
		if !bytes.Equal(before, after) {
			t.Fatal("source changed")
		}
	}
}

func TestPlanInterruptedReadOnlyToolsRejectsUnsafePrefixes(t *testing.T) {
	for _, mode := range []string{"undispatched", "legacy", "write", "delegate", "batch", "active-turn", "completed-uncertain", "completed-confirmed", "evaluation", "error", "worker", "missing-parent", "no-pending", "too-many", "future", "bad-id", "accepted", "mixed-proposal"} {
		t.Run(mode, func(t *testing.T) {
			h := pendingReadOnlyFixture(2)
			now := h[0].Time.Add(time.Minute)
			switch mode {
			case "undispatched":
				h = h[:len(h)-1]
			case "legacy":
				h[3].Data.ToolBehavior = ""
			case "write":
				h[3].Data.ToolBehavior = runtime.BehaviorNonIdempotentWrite
			case "delegate", "batch":
				name := "delegate"
				if mode == "batch" {
					name = "delegate_batch"
				}
				h[2].Data.ToolCalls[1].Name = name
				h[3].Data.ToolName = name
			case "active-turn":
				e := h[1]
				e.ID = "new-turn"
				e.TurnID = "new-turn"
				e.AttemptID = "new-attempt"
				e.Sequence = int64(len(h) + 1)
				h = append(h, e)
			case "completed-uncertain", "completed-confirmed":
				e := h[3]
				e.Kind = runtime.ToolCompleted
				e.ID = "completed"
				e.Sequence = int64(len(h) + 1)
				e.Data.Effect = runtime.ConfirmedEffect
				if mode == "completed-uncertain" {
					e.Data.Effect = runtime.UncertainEffect
				}
				h = append(h, e)
			case "evaluation":
				e := h[3]
				e.Kind = runtime.EvaluationRecorded
				e.ID = "evaluation"
				e.Sequence = int64(len(h) + 1)
				yes := true
				e.Data = runtime.Data{Accepted: &yes}
				h = append(h, e)
			case "error":
				e := h[3]
				e.Kind = runtime.ErrorRecorded
				e.ID = "error"
				e.Sequence = int64(len(h) + 1)
				e.Data = runtime.Data{Code: "failure"}
				h = append(h, e)
			case "worker":
				h[0].WorkerID = "worker"
			case "missing-parent":
				h[0].Data.ParentTaskID = ""
			case "no-pending":
				h = interruptedReadOnlyFixture()[:5]
			case "too-many":
				h = pendingReadOnlyFixture(33)
			case "future":
				h[3].Time = now.Add(time.Second)
			case "bad-id":
				h[3].ID = "bad:id"
			case "accepted":
				yes := true
				h[3].Data.Accepted = &yes
			case "mixed-proposal":
				h[2].Data.ToolCalls = append(h[2].Data.ToolCalls, providers.ToolCall{ID: "unstarted", Name: "read_file", Arguments: json.RawMessage(`{}`)})
			}
			plan, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now)
			if !errors.Is(err, ErrHistory) || !reflect.DeepEqual(plan, InterruptionRecovery{}) {
				t.Fatal("unsafe partial plan", plan, err)
			}
		})
	}
}

func TestInterruptedReadOnlyToolsRejectsForgedSuffix(t *testing.T) {
	for _, mode := range []string{"output", "success-code", "effect", "behavior", "result-id", "result-time", "result-causation", "terminal-id", "terminal-causation", "missing-result", "order"} {
		t.Run(mode, func(t *testing.T) {
			h := pendingReadOnlyFixture(2)
			plan, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, h[0].Time.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			full := append(append([]runtime.Event(nil), h...), plan.Events...)
			first, last := len(h), len(full)-1
			switch mode {
			case "output":
				full[first].Data.Text = "invented output"
			case "success-code":
				full[first].Data.Code = ""
			case "effect":
				full[first].Data.Effect = runtime.UncertainEffect
			case "behavior":
				full[first].Data.ToolBehavior = ""
			case "result-id":
				full[first].ID = "forged"
			case "result-time":
				full[first].Time = full[first].Time.Add(-time.Second)
			case "result-causation":
				full[first].CausationID = "forged"
			case "terminal-id":
				full[last].ID = "forged"
			case "terminal-causation":
				full[last].CausationID = full[first].ID
			case "missing-result":
				full = append(full[:first], full[first+1:]...)
			case "order":
				full[first], full[first+1] = full[first+1], full[first]
			}
			if isInterruptedModelTerminal(full) {
				t.Fatal("forged suffix accepted")
			}
		})
	}
}

func TestInterruptedReadOnlyToolsWorkerAndParentProjection(t *testing.T) {
	h := interruptedWorkerFixture(t)
	h[0] = h[0][:2]
	child := pendingReadOnlyFixture(2)
	for i := range child {
		child[i].TaskID = h[1][0].TaskID
		child[i].SessionID = h[1][0].SessionID
		child[i].CorrelationID = child[i].TaskID
		if child[i].Kind == runtime.TaskStarted || child[i].Kind == runtime.TurnStarted {
			child[i].Data.ModelID = h[1][0].Data.ModelID
			child[i].Data.ProviderID = h[1][0].Data.ProviderID
		}
	}
	child[0].Data.ParentTaskID = h[0][0].TaskID
	child[0].Data.SubmissionID = h[0][0].Data.SubmissionID
	h[1] = child
	plan, err := PlanInterruptedWorkerTree(h, time.Now().UTC().Add(time.Minute))
	if err != nil || plan.Child == nil || len(plan.Child.Events) != 3 {
		t.Fatal(plan, err)
	}
	work := append(append([]runtime.Event(nil), h[0]...), plan.Worker.Events...)
	resolved := append(append([]runtime.Event(nil), child...), plan.Child.Events...)
	result, err := recoveryWorkResult(work, resolved)
	if err != nil || !bytes.Contains(result, []byte("interrupted_read_only_tool")) || bytes.Contains(result, []byte("untrusted_output")) {
		t.Fatal(string(result), err)
	}
	resolved[len(child)].Data.Text = "forged"
	if _, err := PlanInterruptedWorker([][]runtime.Event{h[0], resolved}, time.Now().UTC().Add(time.Minute)); err == nil {
		t.Fatal("worker accepted forged suffix")
	}
}

func TestPlanInterruptedReadOnlyToolsFullSuffixBudgets(t *testing.T) {
	h := pendingReadOnlyFixture(2)
	// Legal model deltas before its completed proposal can fill event budgets.
	delta := h[1]
	delta.Kind = runtime.ModelDelta
	delta.Data = runtime.Data{Text: "x"}
	expanded := append([]runtime.Event(nil), h[:2]...)
	for len(expanded) < 9994 {
		e := delta
		e.ID = fmt.Sprintf("delta-%d", len(expanded))
		expanded = append(expanded, e)
	}
	expanded = append(expanded, h[2:]...)
	for i := range expanded {
		expanded[i].Sequence = int64(i + 1)
	}
	now := h[0].Time.Add(time.Minute)
	if _, err := PlanInterruptedReadOnlyTools([][]runtime.Event{expanded}, now); err != nil {
		t.Fatal("exact event suffix count", len(expanded), err)
	}
	worker := interruptedWorkerFixture(t)[0][:2]
	workerNow := time.Now().UTC().Add(time.Minute)
	treeChild := append([]runtime.Event(nil), expanded...)
	treeChild[0].Data.ParentTaskID = worker[0].TaskID
	treeChild[0].Data.SubmissionID = worker[0].Data.SubmissionID
	for i := range treeChild {
		treeChild[i].TaskID, treeChild[i].CorrelationID = "execution", "execution"
		if treeChild[i].Kind == runtime.TaskStarted || treeChild[i].Kind == runtime.TurnStarted {
			treeChild[i].Data.ModelID, treeChild[i].Data.ProviderID = "model", "provider"
		}
	}
	if _, err := PlanInterruptedReadOnlyTools([][]runtime.Event{treeChild}, workerNow); err != nil {
		t.Fatal("valid child must fit individually", err)
	}
	if _, err := PlanInterruptedWorkerTree([][]runtime.Event{worker, treeChild}, workerNow); err == nil {
		t.Fatal("worker terminal excluded from aggregate event budget")
	}
	fit := append(append([]runtime.Event(nil), treeChild[:2]...), treeChild[5:]...)
	for i := range fit {
		fit[i].Sequence = int64(i + 1)
	}
	if _, err := PlanInterruptedWorkerTree([][]runtime.Event{worker, fit}, workerNow); err != nil {
		t.Fatal("exact worker plus child event budget", err)
	}
	extra := delta
	extra.ID = "extra-delta"
	expanded = append(expanded[:2], append([]runtime.Event{extra}, expanded[2:]...)...)
	for i := range expanded {
		expanded[i].Sequence = int64(i + 1)
	}
	if _, err := PlanInterruptedReadOnlyTools([][]runtime.Event{expanded}, now); err == nil {
		t.Fatal("suffix excluded from event limit")
	}
	h = pendingReadOnlyFixture(2)
	h[2].Data.Text = "x"
	plan, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range append(append([]runtime.Event(nil), h...), plan.Events...) {
		body, _ := e.Encode()
		total += len(body)
	}
	h[2].Data.Text += strings.Repeat("x", (8<<20)-total)
	if _, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now); err != nil {
		t.Fatal("exact byte suffix budget", err)
	}
	h[2].Data.Text += "x"
	if _, err := PlanInterruptedReadOnlyTools([][]runtime.Event{h}, now); err == nil {
		t.Fatal("suffix excluded from byte limit")
	}
}
