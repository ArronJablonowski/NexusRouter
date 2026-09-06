package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func runningWorkerTree(t *testing.T) [][]runtime.Event {
	h := interruptedWorkerFixture(t)
	h[0] = h[0][:2]
	for i, e := range h[1] {
		if e.Kind == runtime.ModelDelta {
			h[1] = h[1][:i+1]
			break
		}
	}
	return h
}

func TestPlanInterruptedWorkerTreePreservesModelPrefix(t *testing.T) {
	for _, boundary := range []string{"start", "turn", "delta", "completed child"} {
		t.Run(boundary, func(t *testing.T) {
			h := runningWorkerTree(t)
			switch boundary {
			case "start":
				h[1] = h[1][:1]
			case "turn":
				h[1] = h[1][:2]
			case "completed child":
				h = interruptedWorkerFixture(t)
			}
			before, _ := json.Marshal(h)
			now := time.Now().UTC()
			plan, err := PlanInterruptedWorkerTree(h, now)
			if err != nil || len(plan.Worker.Events) != 1 || plan.Worker.Events[0].Data.Code != "worker_owner_interrupted" {
				t.Fatal(plan, err)
			}
			child := h[1]
			if boundary == "completed child" {
				if plan.Child != nil {
					t.Fatal("terminal child rewritten")
				}
			} else {
				if plan.Child == nil || len(plan.Child.Events) != 1 || plan.Child.Events[0].Data.Code != "interrupted_model" || plan.Child.Events[0].Data.Text != "" {
					t.Fatal("missing failure-only child plan", plan)
				}
				child = append(append([]runtime.Event(nil), child...), plan.Child.Events...)
				oldState, _ := Replay(context.Background(), terminalReader(h[1]), "child")
				newState, err := Replay(context.Background(), terminalReader(child), "child")
				if err != nil || newState.State != "failed" || newState.InterruptedTurn != oldState.InterruptedTurn || !reflect.DeepEqual(child[:len(child)-1], h[1]) {
					t.Fatal("child history or interruption changed", newState, err)
				}
			}
			worker := append(append([]runtime.Event(nil), h[0]...), plan.Worker.Events...)
			payload, err := recoveryWorkResult(worker, child)
			if err != nil || bytes.Contains(payload, []byte("untrusted_output")) || (plan.Child != nil && !bytes.Contains(payload, []byte("interrupted_model"))) {
				t.Fatal(string(payload), err)
			}
			again, err := PlanInterruptedWorkerTree([][]runtime.Event{h[1], h[0]}, now)
			if err != nil || !reflect.DeepEqual(plan, again) {
				t.Fatal("order-dependent plan", err)
			}
			after, _ := json.Marshal(h)
			if !bytes.Equal(before, after) {
				t.Fatal("mutated source")
			}
		})
	}
}

func TestPlanInterruptedWorkerTreeRejectsUnsafeChild(t *testing.T) {
	for _, mode := range []string{"tool proposal", "pending tool", "uncertain", "evaluation", "worker accepted", "wrong parent", "forged terminal id", "forged terminal text", "forged terminal causation", "extra child"} {
		t.Run(mode, func(t *testing.T) {
			h := runningWorkerTree(t)
			switch mode {
			case "tool proposal":
				h[1][len(h[1])-1].Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)}}
			case "pending tool":
				h[1][len(h[1])-1].Kind = runtime.ToolStarted
				h[1][len(h[1])-1].Data = runtime.Data{ToolCallID: "call", ToolName: "lookup"}
			case "uncertain":
				h[1][0].Data.Effect = runtime.UncertainEffect
			case "evaluation":
				yes := true
				h[1][len(h[1])-1].Kind = runtime.EvaluationRecorded
				h[1][len(h[1])-1].Data.Accepted = &yes
			case "worker accepted":
				h[0] = interruptedWorkerFixture(t)[0]
			case "wrong parent":
				h[1][0].Data.ParentTaskID = "other"
			case "extra child":
				h = append(h, h[1])
			default:
				plan, err := PlanInterruptedModel([][]runtime.Event{h[1]}, time.Now().UTC(), false)
				if err != nil {
					t.Fatal(err)
				}
				e := plan.Events[0]
				switch mode {
				case "forged terminal id":
					e.ID = "forged"
				case "forged terminal text":
					e.Data.Text = "invented"
				case "forged terminal causation":
					e.CausationID = "forged"
				}
				h[1] = append(h[1], e)
			}
			plan, err := PlanInterruptedWorkerTree(h, time.Now().UTC())
			if !errors.Is(err, ErrHistory) || plan.Child != nil || len(plan.Worker.Events) != 0 {
				t.Fatal("unsafe partial plan", plan, err)
			}
		})
	}
}
