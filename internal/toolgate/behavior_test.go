package toolgate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestGateBehaviorBindingAndSingleUse(t *testing.T) {
	for _, behavior := range []runtime.ToolBehavior{runtime.BehaviorIdempotentWrite, runtime.BehaviorNonIdempotentWrite} {
		t.Run(string(behavior), func(t *testing.T) {
			ctx := context.Background()
			db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
				e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
				if i > 0 {
					e.TurnID = "turn"
					e.AttemptID = "attempt"
				}
				if kind == runtime.TurnCompleted {
					e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write_file", Arguments: []byte(`{}`)}}
				}
				if kind == runtime.ToolStarted {
					e.Data = runtime.Data{ToolCallID: "call", ToolName: "write_file", Effect: runtime.UncertainEffect, ToolBehavior: behavior}
				}
				if err = db.Append(ctx, int64(i), e); err != nil {
					t.Fatal(err)
				}
			}
			reviews, executions := 0, 0
			gate := Gate{Store: db, Review: func(_ context.Context, r approvals.Request) (string, bool, error) {
				reviews++
				if r.ToolBehavior != behavior {
					t.Fatal("behavior not presented", r)
				}
				return "operator", true, nil
			}}
			a := tools.Authorization{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "write_file", ToolBehavior: behavior, Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64)}
			handler := func(context.Context) (runtime.ToolResult, error) {
				executions++
				return runtime.ToolResult{Effect: runtime.UncertainEffect}, errors.New("fixture uncertain")
			}
			for _, wrong := range []runtime.ToolBehavior{"", runtime.BehaviorReadOnly} {
				proposal := a
				proposal.ToolBehavior = wrong
				if _, err := gate.ExecuteApproved(ctx, proposal, handler); !errors.Is(err, tools.ErrDenied) {
					t.Fatal(err)
				}
			}
			if reviews != 0 || executions != 0 {
				t.Fatal("mismatch reached reviewer/handler")
			}
			out, err := gate.ExecuteApproved(ctx, a, handler)
			if !errors.Is(err, tools.ErrExecution) || out.Effect != runtime.UncertainEffect || executions != 1 || reviews != 1 {
				t.Fatal(out, err, executions, reviews)
			}
			if _, err = gate.ExecuteApproved(ctx, a, handler); !errors.Is(err, tools.ErrDenied) || executions != 1 {
				t.Fatal("uncertain idempotent write repeated", err, executions)
			}
		})
	}
}
