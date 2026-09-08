package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func interruptedDelegationFixture(t *testing.T) [][]runtime.Event {
	t.Helper()
	h := terminalTreeFixture(t)
	var turn runtime.Event
	for _, e := range h[0] {
		if e.Kind == runtime.TurnStarted {
			turn = e
			break
		}
	}
	turn.Sequence = 2
	completed := turn
	completed.Kind, completed.Sequence, completed.ID = runtime.TurnCompleted, 3, "parent-proposal"
	completed.Data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "delegate-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"Return answer","validation":"text"}`)}}, FinishReason: "tool_calls"}
	started := turn
	started.Kind, started.Sequence, started.ID = runtime.ToolStarted, 4, "parent-dispatched"
	started.Data = runtime.Data{ToolName: "delegate", ToolCallID: "delegate-call", Effect: runtime.UncertainEffect}
	h[0] = []runtime.Event{h[0][0], turn, completed, started}
	h[1][0].Data.DelegationOrigin = &runtime.DelegationOrigin{Version: 1, TurnID: turn.TurnID, AttemptID: turn.AttemptID, ToolCallID: "delegate-call", ToolName: "delegate"}
	return h
}

func cloneRecoveryHistory(t *testing.T, h []runtime.Event, id, session, parent string) []runtime.Event {
	t.Helper()
	body, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var out []runtime.Event
	if json.Unmarshal(body, &out) != nil {
		t.Fatal("clone")
	}
	for i := range out {
		out[i].TaskID = id
		out[i].SessionID = session
		out[i].CorrelationID = id
		out[i].ID = fmt.Sprintf("%s-%d", id, i)
		if out[i].WorkerID != "" {
			out[i].WorkerID = "worker-" + id
		}
	}
	out[0].Data.ParentTaskID = parent
	return out
}

func TestPlanInterruptedDelegationCompletesToolNotParent(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			h := interruptedDelegationFixture(t)
			before, _ := json.Marshal(h)
			now := time.Now().UTC()
			plan, err := PlanInterruptedDelegation(h, now, canceled)
			if err != nil || plan.ParentTaskID != "task" || plan.ExpectedSequence != 4 || len(plan.Events) != 2 {
				t.Fatal(plan, err)
			}
			tool, end := plan.Events[0], plan.Events[1]
			if tool.Kind != runtime.ToolCompleted || tool.Sequence != 5 || tool.Data.ToolCallID != "delegate-call" || tool.Data.ToolName != "delegate" || tool.Data.Effect != runtime.NoEffect || tool.Data.Code != "delegation_recovered" || tool.TurnID != h[0][3].TurnID || tool.AttemptID != h[0][3].AttemptID {
				t.Fatal("incorrect recovered proposal", tool)
			}
			if end.Sequence != 6 || end.CausationID != tool.ID || (!canceled && (end.Kind != runtime.TaskFailed || end.Data.Code != "interrupted_after_delegation")) || (canceled && (end.Kind != runtime.TaskCanceled || end.Data.Code != "canceled")) {
				t.Fatal("parent recovery misreported success", end)
			}
			var result struct {
				Work      string `json:"work_task_id"`
				Execution string `json:"execution_task_id"`
				Output    string `json:"untrusted_output"`
			}
			if json.Unmarshal([]byte(tool.Data.Text), &result) != nil || result.Work != "work" || result.Execution != "child" || result.Output != "answer" {
				t.Fatal("incorrect accepted work", tool.Data.Text)
			}
			after, _ := json.Marshal(h)
			if !bytes.Equal(before, after) {
				t.Fatal("planner mutated input")
			}
			again, err := PlanInterruptedDelegation(h, now, canceled)
			encoded, _ := json.Marshal(plan)
			repeated, _ := json.Marshal(again)
			if err != nil || !bytes.Equal(encoded, repeated) {
				t.Fatal("same input did not reproduce plan")
			}
		})
	}
}

func TestPlanInterruptedDelegationBatchMixedOutcomes(t *testing.T) {
	h := interruptedDelegationFixture(t)
	h[0][2].Data.ToolCalls[0].Name = "delegate_batch"
	h[0][2].Data.ToolCalls[0].Arguments = json.RawMessage(`{"tasks":[{"prompt":"A","validation":"text"},{"prompt":"B","validation":"text"}]}`)
	h[0][3].Data.ToolName = "delegate_batch"
	zero := 0
	h[1][0].Data.DelegationOrigin.ToolName = "delegate_batch"
	h[1][0].Data.DelegationOrigin.BatchIndex = &zero
	w := cloneRecoveryHistory(t, h[1], "work-two", "task", "task")
	one := 1
	w[0].Data.DelegationOrigin.BatchIndex = &one
	failed := w[4]
	failed.Sequence = 3
	failed.Kind = runtime.TaskFailed
	failed.Data = runtime.Data{Code: "worker_failed", Text: "PRIVATE_WORK_ERROR"}
	w = append(w[:2], failed)
	h = append(h, w)
	before, _ := json.Marshal(h)
	plan, err := PlanInterruptedDelegation(h, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(h)
	if !bytes.Equal(before, after) {
		t.Fatal("batch argument decoding mutated source history")
	}
	var result struct {
		Results []map[string]any `json:"results"`
	}
	if json.Unmarshal([]byte(plan.Events[0].Data.Text), &result) != nil || len(result.Results) != 2 || result.Results[0]["untrusted_output"] != "answer" || result.Results[1]["reason"] != "worker_failed" || result.Results[1]["work_task_id"] != "work-two" || strings.Contains(plan.Events[0].Data.Text, "PRIVATE_") {
		t.Fatal("batch ordering/evidence incorrect", plan.Events[0].Data.Text)
	}
	if _, ok := result.Results[1]["execution_task_id"]; ok {
		t.Fatal("missing execution invented")
	}
}

func TestPlanInterruptedDelegationValidatesEarlierCompletedWork(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprint(tampered), func(t *testing.T) {
			h := interruptedDelegationFixture(t)
			first, err := PlanInterruptedDelegation(h, time.Now(), false)
			if err != nil {
				t.Fatal(err)
			}
			completed := first.Events[0]
			completed.Data.Code = ""
			if tampered {
				completed.Data.Text = `{"work_task_id":"work","execution_task_id":"child","untrusted_output":"tampered"}`
			}
			h[0] = append(h[0], completed)
			turn := h[0][1]
			turn.TurnID = "next-turn"
			turn.AttemptID = "next-attempt"
			turn.Sequence = 6
			turn.ID = "next-turn-event"
			h[0] = append(h[0], turn)
			proposal := h[0][2]
			proposal.TurnID = turn.TurnID
			proposal.AttemptID = turn.AttemptID
			proposal.Sequence = 7
			proposal.ID = "next-proposal"
			proposal.Data.ToolCalls = []providers.ToolCall{{ID: "next-call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"Again","validation":"text"}`)}}
			h[0] = append(h[0], proposal)
			dispatched := h[0][3]
			dispatched.TurnID = turn.TurnID
			dispatched.AttemptID = turn.AttemptID
			dispatched.Sequence = 8
			dispatched.ID = "next-dispatched"
			dispatched.Data.ToolCallID = "next-call"
			h[0] = append(h[0], dispatched)
			work := cloneRecoveryHistory(t, h[1], "next-work", "task", "task")
			work[0].Data.DelegationOrigin = &runtime.DelegationOrigin{Version: 1, TurnID: turn.TurnID, AttemptID: turn.AttemptID, ToolCallID: "next-call", ToolName: "delegate"}
			execution := cloneRecoveryHistory(t, h[2], "next-execution", "next-execution", "next-work")
			h = append(h, work, execution)
			plan, err := PlanInterruptedDelegation(h, time.Now(), false)
			if tampered {
				if err == nil || len(plan.Events) != 0 {
					t.Fatal("earlier forged result ignored")
				}
			} else if err != nil || plan.ExpectedSequence != 8 {
				t.Fatal(plan, err)
			}
		})
	}
}

func TestPlanInterruptedDelegationRejectsUnprovenRecovery(t *testing.T) {
	for _, mode := range []string{"undispatched", "no work", "missing execution", "missing origin", "wrong origin call", "wrong origin turn", "wrong origin attempt", "wrong origin tool", "wrong session", "wrong parent", "orphan", "duplicate work", "two executions", "nonterminal child", "worker output mismatch", "unknown failure", "other pending", "prior uncertain", "argument duplicate", "argument malformed", "too many tasks", "oversized", "zero time"} {
		t.Run(mode, func(t *testing.T) {
			h := interruptedDelegationFixture(t)
			now := time.Now()
			switch mode {
			case "undispatched":
				h[0] = h[0][:3]
			case "no work":
				h = [][]runtime.Event{h[0]}
			case "missing execution":
				h = h[:2]
			case "missing origin":
				h[1][0].Data.DelegationOrigin = nil
			case "wrong origin call":
				h[1][0].Data.DelegationOrigin.ToolCallID = "other"
			case "wrong origin turn":
				h[1][0].Data.DelegationOrigin.TurnID = "other"
			case "wrong origin attempt":
				h[1][0].Data.DelegationOrigin.AttemptID = "other"
			case "wrong origin tool":
				index := 0
				h[1][0].Data.DelegationOrigin.ToolName = "delegate_batch"
				h[1][0].Data.DelegationOrigin.BatchIndex = &index
			case "wrong session":
				for i := range h[1] {
					h[1][i].SessionID = "other"
				}
			case "wrong parent":
				h[1][0].Data.ParentTaskID = "other"
			case "orphan":
				h[2][0].Data.ParentTaskID = "missing"
			case "duplicate work":
				h = append(h, cloneRecoveryHistory(t, h[1], "duplicate-work", "task", "task"))
			case "two executions":
				h = append(h, cloneRecoveryHistory(t, h[2], "duplicate-execution", "other", "work"))
			case "nonterminal child":
				h[2] = h[2][:len(h[2])-1]
			case "worker output mismatch":
				h[1][3].Data.Text = "wrong"
			case "unknown failure":
				h[1][4].Kind = runtime.TaskFailed
				h[1][4].Data.Code = "PRIVATE_UNKNOWN"
			case "other pending":
				h[0][2].Data.ToolCalls = append(h[0][2].Data.ToolCalls, providers.ToolCall{ID: "other", Name: "read", Arguments: json.RawMessage(`{}`)})
			case "prior uncertain":
				e := h[0][3]
				e.Kind = runtime.ToolCompleted
				e.Sequence = 5
				e.ID = "uncertain"
				e.Data.Effect = runtime.UncertainEffect
				h[0] = append(h[0], e)
			case "argument duplicate":
				h[0][2].Data.ToolCalls[0].Arguments = json.RawMessage(`{"prompt":"A","prompt":"B","validation":"text"}`)
			case "argument malformed":
				h[0][2].Data.ToolCalls[0].Arguments = json.RawMessage(`{"prompt":"A"}`)
			case "too many tasks":
				for len(h) < 67 {
					h = append(h, h[1])
				}
			case "oversized":
				h[0][0].Data.Text = strings.Repeat("x", 8<<20)
			case "zero time":
				now = time.Time{}
			}
			plan, err := PlanInterruptedDelegation(h, now, false)
			if err == nil || len(plan.Events) != 0 {
				t.Fatal("unproven recovery planned", mode, plan)
			}
		})
	}
}

func TestPlanInterruptedDelegationRejectsFailedChildToolUncertainty(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint(finished), func(t *testing.T) {
			h := interruptedDelegationFixture(t)
			failed := h[1][4]
			failed.Kind = runtime.TaskFailed
			failed.Sequence = 3
			failed.Data = runtime.Data{Code: "worker_failed"}
			h[1] = append(h[1][:2], failed)
			child := h[2]
			var start runtime.Event
			for _, e := range child {
				if e.Kind == runtime.TurnStarted {
					start = e
					break
				}
			}
			start.Sequence = 2
			proposal := start
			proposal.ID = "child-proposal"
			proposal.Kind = runtime.TurnCompleted
			proposal.Sequence = 3
			proposal.Data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "child-call", Name: "write", Arguments: json.RawMessage(`{}`)}}, FinishReason: "tool_calls"}
			dispatch := start
			dispatch.ID = "child-dispatch"
			dispatch.Kind = runtime.ToolStarted
			dispatch.Sequence = 4
			dispatch.Data = runtime.Data{ToolCallID: "child-call", ToolName: "write", Effect: runtime.UncertainEffect}
			child = []runtime.Event{child[0], start, proposal, dispatch}
			if finished {
				result := dispatch
				result.ID = "child-uncertain"
				result.Kind = runtime.ToolCompleted
				result.Sequence = 5
				child = append(child, result)
			}
			end := start
			end.ID = "child-failed"
			end.Kind = runtime.TaskFailed
			end.Sequence = int64(len(child) + 1)
			end.Data = runtime.Data{Code: "execution_failed"}
			h[2] = append(child, end)
			if _, err := ProjectTerminalSubmission(h[2]); err != nil {
				t.Fatal("fixture must be valid terminal-but-uncertain", err)
			}
			snapshot, err := Replay(context.Background(), terminalReader(h[2]), h[2][0].TaskID)
			if err != nil || !snapshot.UncertainEffects {
				t.Fatal("fixture did not retain uncertainty")
			}
			if plan, err := PlanInterruptedDelegation(h, time.Now(), false); err == nil || len(plan.Events) != 0 {
				t.Fatal("failed child tool uncertainty cleared")
			}
		})
	}
}

func TestRecoveryWorkResultPreservesProducerOutputBounds(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", (64<<10)+1), "\xff", "   "} {
		h := interruptedDelegationFixture(t)
		h[1][3].Data.Text = text
		for i := range h[2] {
			if h[2][i].Kind == runtime.TurnCompleted {
				h[2][i].Data.Text = text
			}
		}
		if _, err := recoveryWorkResult(h[1], h[2]); err == nil {
			t.Fatal("producer-rejected output recovered")
		}
	}
	h := interruptedDelegationFixture(t)
	text := strings.Repeat("<", 22000)
	h[1][3].Data.Text = text
	for i := range h[2] {
		if h[2][i].Kind == runtime.TurnCompleted {
			h[2][i].Data.Text = text
		}
	}
	encoded, err := recoveryWorkResult(h[1], h[2])
	if err != nil || len(encoded) <= 128<<10 {
		t.Fatal("fixture must exceed batch encoded-item cap", err)
	}
	h[0][2].Data.ToolCalls[0].Name = "delegate_batch"
	h[0][2].Data.ToolCalls[0].Arguments = json.RawMessage(`{"tasks":[{"prompt":"A","validation":"text"},{"prompt":"B","validation":"text"}]}`)
	h[0][3].Data.ToolName = "delegate_batch"
	zero := 0
	h[1][0].Data.DelegationOrigin.ToolName = "delegate_batch"
	h[1][0].Data.DelegationOrigin.BatchIndex = &zero
	w := cloneRecoveryHistory(t, h[1], "other-work", "task", "task")
	one := 1
	w[0].Data.DelegationOrigin.BatchIndex = &one
	e := cloneRecoveryHistory(t, h[2], "other-execution", "other-execution", "other-work")
	h = append(h, w, e)
	if plan, err := PlanInterruptedDelegation(h, time.Now(), false); err == nil || len(plan.Events) != 0 {
		t.Fatal("batch producer encoded bound bypassed")
	}
}

func TestRecoveryWorkResultPreservesAndValidatesDelegationAudit(t *testing.T) {
	h := interruptedDelegationFixture(t)
	intent := &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	confidence := .625
	h[1][0].Data.DelegationAuditIntent = intent
	h[1][3].Data.DelegationAudit = &runtime.DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", AuditID: "audit", Status: "completed", Verdict: "accept", Confidence: &confidence, Citations: []string{"candidate"}}
	body, err := recoveryWorkResult(h[1], h[2])
	if err != nil || !bytes.Contains(body, []byte(`"audit":{"status":"completed","verdict":"accept","confidence":0.625,"cited_evidence":["candidate"]}`)) || bytes.Contains(body, []byte("operation")) {
		t.Fatal(string(body), err)
	}
	for _, mutate := range []func([]runtime.Event){
		func(work []runtime.Event) { work[3].Data.DelegationAudit = nil },
		func(work []runtime.Event) { work[3].Data.DelegationAudit.OperationID = "other" },
		func(work []runtime.Event) { work[0].Data.DelegationAuditIntent.ReviewerID = "other" },
		func(work []runtime.Event) { work[3].Data.DelegationAudit.ReviewerID = "other" },
		func(work []runtime.Event) { work[0].Data.DelegationAuditIntent = nil },
	} {
		work := append([]runtime.Event(nil), h[1]...)
		work[0].Data = h[1][0].Data
		work[3].Data = h[1][3].Data
		if work[0].Data.DelegationAuditIntent != nil {
			intent := *work[0].Data.DelegationAuditIntent
			work[0].Data.DelegationAuditIntent = &intent
		}
		if work[3].Data.DelegationAudit != nil {
			work[3].Data.DelegationAudit = work[3].Data.DelegationAudit.Clone()
		}
		mutate(work)
		if _, err := recoveryWorkResult(work, h[2]); err == nil {
			t.Fatal("unbound audit recovered")
		}
	}
}
