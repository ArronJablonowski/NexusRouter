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

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func workerWithoutChildFixture(t *testing.T) []runtime.Event {
	t.Helper()
	h := interruptedWorkerFixture(t)[0][:2]
	heartbeat := h[1]
	heartbeat.Kind, heartbeat.ID, heartbeat.Sequence = runtime.WorkerHeartbeat, "heartbeat", 3
	return append(append([]runtime.Event(nil), h...), heartbeat)
}

func TestPlanInterruptedWorkerWithoutChildPreservesPrefix(t *testing.T) {
	for _, mode := range []string{"task-start-only", "task-start-unsubmitted", "started", "heartbeat", "unsubmitted", "batch"} {
		t.Run(mode, func(t *testing.T) {
			h := workerWithoutChildFixture(t)
			switch mode {
			case "task-start-only":
				h = h[:1]
			case "task-start-unsubmitted":
				h = h[:1]
				h[0].Data.SubmissionID = ""
			case "started":
				h = h[:2]
			case "unsubmitted":
				h[0].Data.SubmissionID = ""
			case "batch":
				index := 2
				h[0].Data.DelegationOrigin = h[0].Data.DelegationOrigin.Clone()
				h[0].Data.DelegationOrigin.ToolName = "delegate_batch"
				h[0].Data.DelegationOrigin.BatchIndex = &index
			}
			before, _ := json.Marshal(h)
			now := time.Now().UTC()
			plan, err := PlanInterruptedWorkerWithoutChild(h, now)
			if err != nil || plan.ParentTaskID != h[0].TaskID || plan.ExpectedSequence != int64(len(h)) || len(plan.Events) != 1 {
				t.Fatal(plan, err)
			}
			end := plan.Events[0]
			if end.Kind != runtime.TaskFailed || end.Data.Code != "worker_owner_interrupted" || end.Data.Text != "" || end.Data.Accepted != nil || end.CausationID != h[len(h)-1].ID || end.Data.SubmissionID != "" {
				t.Fatal("not failure-only", end)
			}
			again, err := PlanInterruptedWorkerWithoutChild(h, now)
			if err != nil || !reflect.DeepEqual(again, plan) {
				t.Fatal("nondeterministic", err)
			}
			tree, err := PlanInterruptedWorkerTree([][]runtime.Event{h}, now)
			if err != nil || tree.Child != nil || !reflect.DeepEqual(tree.Worker, plan) {
				t.Fatal("tree forwarding", err)
			}
			// The terminal ID matches the established two-history algorithm.
			paired := interruptedWorkerFixture(t)
			paired[0] = h
			paired[1][0].Data.SubmissionID = h[0].Data.SubmissionID
			if len(h) > 1 {
				old, err := PlanInterruptedWorker(paired, now.Add(time.Second))
				newPlan, newErr := PlanInterruptedWorkerWithoutChild(h, now.Add(time.Second))
				if err != nil || newErr != nil || !reflect.DeepEqual(old, newPlan) {
					t.Fatal("terminal algorithm diverged", err, newErr)
				}
			}
			full := append(append([]runtime.Event(nil), h...), end)
			state, err := Replay(context.Background(), terminalReader(full), h[0].TaskID)
			if err != nil || state.State != "failed" || state.UncertainEffects || state.InterruptedTurn {
				t.Fatal(state, err)
			}
			after, _ := json.Marshal(h)
			if !bytes.Equal(before, after) {
				t.Fatal("mutated source")
			}
		})
	}
}

func TestPlanInterruptedWorkerWithoutChildRejectsUnsafeMetadata(t *testing.T) {
	for _, mode := range []string{"empty", "missing-started", "duplicate-started", "evaluation", "output", "model", "tool", "steering", "error", "terminal", "effect-none", "effect-uncertain", "hidden-data", "privacy", "retry", "origin", "batch", "parent", "worker", "task", "session", "correlation", "turn", "attempt", "route", "causation", "sequence", "duplicate-id", "invalid-id", "future", "backward", "zero-now", "overflow-now"} {
		t.Run(mode, func(t *testing.T) {
			h := workerWithoutChildFixture(t)
			now := time.Now().UTC()
			switch mode {
			case "empty":
				h = nil
			case "missing-started":
				h[1].Kind = runtime.WorkerHeartbeat
			case "duplicate-started":
				h[2].Kind = runtime.WorkerStarted
			case "evaluation":
				yes := true
				h[2].Kind = runtime.EvaluationRecorded
				h[2].Data = runtime.Data{Accepted: &yes, Code: "worker_validator"}
			case "output":
				h[2].Kind = runtime.WorkerCompleted
				h[2].Data.Text = "unaccepted output"
			case "model":
				h[2].Kind = runtime.ModelDelta
				h[2].TurnID = "turn"
			case "tool":
				h[2].Kind = runtime.ToolStarted
				h[2].TurnID = "turn"
				h[2].Data = runtime.Data{ToolName: "read_file", ToolCallID: "call", Effect: runtime.NoEffect}
			case "steering":
				h[2].Kind = runtime.SteeringApplied
				h[2].Data = runtime.Data{SteeringID: "steer", Text: "change"}
			case "error":
				h[2].Kind = runtime.ErrorRecorded
				h[2].Data.Code = "error"
			case "terminal":
				h[2].Kind = runtime.TaskFailed
				h[2].Data.Code = "worker_failed"
			case "effect-none":
				h[2].Data.Effect = runtime.NoEffect
			case "effect-uncertain":
				h[2].Data.Effect = runtime.UncertainEffect
			case "hidden-data":
				h[2].Data.ConfigID = "hidden"
			case "privacy":
				h[0].Data.Privacy = "local_only"
			case "retry":
				h[0].Data.RetryOfTaskID = "other"
			case "origin":
				h[0].Data.DelegationOrigin = nil
			case "batch":
				h[0].Data.DelegationOrigin = h[0].Data.DelegationOrigin.Clone()
				h[0].Data.DelegationOrigin.ToolName = "delegate_batch"
			case "parent":
				h[0].Data.ParentTaskID = h[0].TaskID
			case "worker":
				h[2].WorkerID = "other"
			case "task":
				h[2].TaskID = "other"
			case "session":
				h[2].SessionID = "other"
			case "correlation":
				h[2].CorrelationID = "other"
			case "turn":
				h[2].TurnID = "hidden"
			case "attempt":
				h[2].AttemptID = "hidden"
			case "route":
				h[2].RouteID = "hidden"
			case "causation":
				h[2].CausationID = "hidden"
			case "sequence":
				h[2].Sequence++
			case "duplicate-id":
				h[2].ID = h[1].ID
			case "invalid-id":
				h[2].ID = "unsafe:id"
			case "future":
				h[2].Time = now.Add(time.Second)
			case "backward":
				h[2].Time = h[0].Time.Add(-time.Second)
			case "zero-now":
				now = time.Time{}
			case "overflow-now":
				now = time.Date(2260, 12, 31, 23, 30, 0, 0, time.FixedZone("negative", -3600))
			}
			before, _ := json.Marshal(h)
			plan, err := PlanInterruptedWorkerWithoutChild(h, now)
			if !errors.Is(err, ErrHistory) || !reflect.DeepEqual(plan, InterruptionRecovery{}) {
				t.Fatal("unsafe partial plan", plan, err)
			}
			after, _ := json.Marshal(h)
			if !bytes.Equal(before, after) {
				t.Fatal("rejection changed history")
			}
		})
	}
}

func TestPlanInterruptedWorkerWithoutChildAggregateBudgets(t *testing.T) {
	h := workerWithoutChildFixture(t)
	for len(h) < 9999 {
		h = append(h, h[2])
	}
	for i := range h {
		h[i].Sequence = int64(i + 1)
		h[i].ID = fmt.Sprintf("event-%d", i)
	}
	now := time.Now().UTC()
	if _, err := PlanInterruptedWorkerWithoutChild(h, now); err != nil {
		t.Fatal("exact event count", err)
	}
	extra := append(append([]runtime.Event(nil), h...), h[2])
	extra[9999].Sequence = 10000
	extra[9999].ID = "too-many"
	if _, err := PlanInterruptedWorkerWithoutChild(extra, now); !errors.Is(err, ErrHistory) {
		t.Fatal("failure excluded from count", err)
	}
	for i := range h {
		h[i].TaskID = strings.Repeat("t", 128)
		h[i].CorrelationID = h[i].TaskID
		h[i].SessionID = strings.Repeat("s", 128)
		h[i].WorkerID = strings.Repeat("w", 128)
	}
	base, err := PlanInterruptedWorkerWithoutChild(h, now)
	if err != nil {
		t.Fatal("byte fixture base", err)
	}
	encoded, _ := base.Events[0].Encode()
	remaining := (8 << 20) - len(encoded)
	for _, e := range h {
		body, _ := e.Encode()
		remaining -= len(body)
	}
	// Fill only earlier IDs: the terminal's causation/ID lengths stay fixed.
	for i := 0; i < len(h)-1 && remaining > 0; i++ {
		capacity := 128 - len(h[i].ID)
		// '<' is a valid ID byte but costs six JSON-escaped bytes.
		escaped := min(capacity, remaining/6)
		plain := min(capacity-escaped, remaining-6*escaped)
		h[i].ID += strings.Repeat("<", escaped) + strings.Repeat("x", plain)
		remaining -= 6*escaped + plain
	}
	if remaining != 0 {
		t.Fatal("could not fill byte budget", remaining)
	}
	if _, err := PlanInterruptedWorkerWithoutChild(h, now); err != nil {
		t.Fatal("exact byte limit", err)
	}
	for i := 0; i < len(h)-1; i++ {
		if len(h[i].ID) < 128 {
			h[i].ID += "x"
			break
		}
	}
	if _, err := PlanInterruptedWorkerWithoutChild(h, now); !errors.Is(err, ErrHistory) {
		t.Fatal("failure excluded from byte budget", err)
	}
}
