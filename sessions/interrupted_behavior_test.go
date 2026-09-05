package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestInterruptedDelegationPreservesBehavior(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly, runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		for _, canceled := range []bool{false, true} {
			t.Run(string(behavior)+"/"+map[bool]string{false: "failed", true: "canceled"}[canceled], func(t *testing.T) {
				h := interruptedDelegationFixture(t)
				h[0][3].Data.ToolBehavior = behavior
				before, _ := json.Marshal(h)
				plan, err := PlanInterruptedDelegation(h, time.Now().UTC(), canceled)
				if err != nil || len(plan.Events) != 2 {
					t.Fatal("declared delegation could not recover", err)
				}
				tool := plan.Events[0]
				if tool.Kind != runtime.ToolCompleted || tool.Data.ToolBehavior != behavior || tool.Data.Effect != runtime.NoEffect || tool.Data.Code != "delegation_recovered" {
					t.Fatal("recovery lost declaration or changed observed outcome")
				}
				after, _ := json.Marshal(h)
				if !bytes.Equal(before, after) {
					t.Fatal("recovery mutated source history")
				}
				h[0] = append(h[0], plan.Events...)
				if _, err := ProjectTerminalTree(h); err != nil {
					t.Fatal("recovered tree rejected", err)
				}
				snapshot, err := Replay(context.Background(), terminalReader(h[0]), h[0][0].TaskID)
				if err != nil || len(snapshot.Pending) != 0 || snapshot.UncertainEffects || snapshot.State == "completed" {
					t.Fatal("recovery altered terminal or effect semantics", err)
				}
				// Removing or changing the declaration cannot turn a synthetic
				// completion into valid evidence for a differently typed call.
				wrong := runtime.BehaviorReadOnly
				if behavior == wrong {
					wrong = ""
				}
				h[0][4].Data.ToolBehavior = wrong
				if _, err := ProjectTerminalTree(h); err == nil {
					t.Fatal("mismatched recovered declaration accepted")
				}
				if _, err := Replay(context.Background(), terminalReader(h[0]), h[0][0].TaskID); err == nil {
					t.Fatal("mismatched recovered declaration replayed")
				}
			})
		}
	}
}
