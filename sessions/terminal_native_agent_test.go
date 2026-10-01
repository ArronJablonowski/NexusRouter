package sessions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type terminalNativeTools struct{ mode string }

func (terminalNativeTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped")
}
func (t terminalNativeTools) ExecuteScoped(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
	if t.mode == "uncertain" {
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, errors.New("unknown")
	}
	if t.mode == "recoverable" {
		return runtime.ToolResult{Effect: runtime.NoEffect, Failed: true, Recoverable: true, Content: "tool failed"}, nil
	}
	return runtime.ToolResult{Effect: runtime.ConfirmedEffect, Content: "effect recorded"}, nil
}
func (terminalNativeTools) ToolBehavior(string) runtime.ToolBehavior {
	return runtime.BehaviorNonIdempotentWrite
}
func nativeAgentTerminalHistory(t *testing.T, mode string) []runtime.Event {
	t.Helper()
	var j terminalJournal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "agent-v1", Provider: "provider", Model: "model", ModelRevision: "revision", ConfigSHA256: strings.Repeat("a", 64)}
	r := runtime.HarnessAgentRequest{Request: runtime.HarnessRequest{TaskID: "agent", SessionID: "agent", SubmissionID: "submission", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "hard"}}}, MaxTurns: 2, Tools: terminalNativeTools{mode}}
	r.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		var usage *providers.Usage
		if mode != "unknown" {
			usage = &providers.Usage{InputTokens: 10, OutputTokens: 3}
			if mode == "zero" {
				usage = &providers.Usage{}
			}
		}
		turn, attempt, e := s.BeginTurn(ctx)
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		calls, e := s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Calls: []providers.ToolCall{{ID: "call", Name: "write", Arguments: []byte(`{}`)}}, Usage: usage})
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		if _, e = s.Invoke(ctx, calls[0]); e != nil {
			return runtime.HarnessOutput{}, e
		}
		if mode == "failed" {
			return runtime.HarnessOutput{}, errors.New("failed after effect")
		}
		if mode == "canceled" {
			cancel()
			return runtime.HarnessOutput{}, ctx.Err()
		}
		turn, attempt, e = s.BeginTurn(ctx)
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		_, e = s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Text: "final", Usage: usage})
		return runtime.HarnessOutput{Actual: identity, Text: "final"}, e
	}
	_, _, err := runtime.RunHarnessAgent(ctx, &j, r)
	failed := mode == "failed" || mode == "canceled" || mode == "uncertain"
	if (err != nil) != failed {
		t.Fatal(mode, err)
	}
	return j
}
func TestProjectNativeAgentTerminal(t *testing.T) {
	for _, mode := range []string{"completed", "zero", "unknown", "failed", "canceled", "uncertain", "recoverable"} {
		t.Run(mode, func(t *testing.T) {
			events := nativeAgentTerminalHistory(t, mode)
			out, err := ProjectTerminalSubmission(events)
			if err != nil || out.Result == nil || out.Result.HarnessEvidenceStatus != "not_recovered" || out.Result.AuditStatus != "not_recovered" {
				t.Fatal(out, err)
			}
			failed := mode == "failed" || mode == "uncertain" || mode == "canceled"
			if failed {
				want := "failed"
				if mode == "canceled" {
					want = "canceled"
				}
				if out.State != want || out.Result.Text != "" || out.Result.FinishReason != "" || out.Result.Turns != 1 {
					t.Fatal(out)
				}
			} else if out.State != "succeeded" || out.Result.Text != "final" || out.Result.Turns != 2 || out.Result.FinishReason != "stop" {
				t.Fatal(out)
			}
			if mode == "unknown" {
				if out.Result.Usage != nil {
					t.Fatal("fabricated usage")
				}
			} else {
				want := int64(20)
				if failed {
					want = 10
				}
				if mode == "zero" {
					want = 0
				}
				if out.Result.Usage == nil || out.Result.Usage.InputTokens != want {
					t.Fatal(out.Result)
				}
			}
			if mode == "recoverable" {
				snapshot, e := Replay(context.Background(), terminalReader(events), "agent")
				if e != nil {
					t.Fatal(e)
				}
				seen := false
				for _, m := range snapshot.Messages {
					if m.Role == "tool" {
						seen = true
						if !m.ToolFailed {
							t.Fatal("recoverable failure became success")
						}
					}
				}
				if !seen {
					t.Fatal("missing tool message")
				}
			}
		})
	}
}
func TestInterruptedNativeAgentPreservesEveryBoundary(t *testing.T) {
	events := nativeAgentTerminalHistory(t, "completed")
	now := events[len(events)-1].Time.Add(time.Second)
	for n := 1; n < len(events); n++ {
		for _, canceled := range []bool{false, true} {
			prefix := events[:n]
			plan, err := PlanInterruptedNativeAgent([][]runtime.Event{prefix}, now, canceled)
			if err != nil || len(plan.Events) != 1 || plan.ExpectedSequence != int64(n) {
				t.Fatal(n, canceled, plan, err)
			}
			last := plan.Events[0]
			if last.Kind == runtime.ToolCompleted || last.Data.Text != "" || last.Data.HarnessOutcome != nil || last.CausationID != prefix[n-1].ID {
				t.Fatal("invented tool result", last)
			}
			full := append(append([]runtime.Event(nil), prefix...), last)
			before, e := Replay(context.Background(), terminalReader(prefix), "agent")
			if e != nil {
				t.Fatal(e)
			}
			after, e := Replay(context.Background(), terminalReader(full), "agent")
			if e != nil || !reflect.DeepEqual(before.Pending, after.Pending) || before.UncertainEffects != after.UncertainEffects || before.InterruptedTurn != after.InterruptedTurn {
				t.Fatal("rewrote effect uncertainty", n, e)
			}
			projected, e := ProjectTerminalSubmission(full)
			if e != nil || projected.Result.Text != "" || projected.State == "succeeded" {
				t.Fatal("recovered uncommitted output", n, projected, e)
			}
			if _, e := runtime.ValidateHarnessOutcome(full, "agent"); e == nil {
				t.Fatal("interruption gained quality outcome")
			}
			if _, e := PlanInterruptedNativeAgent([][]runtime.Event{full}, now, false); e == nil {
				t.Fatal("recovered terminal twice")
			}
			if _, e := PlanInterruptedModel([][]runtime.Event{prefix}, now, false); e == nil {
				t.Fatal("native agent used legacy model recovery")
			}
		}
	}
}
func TestInterruptedNativeAgentRejectsUnboundHistory(t *testing.T) {
	for _, mode := range []string{"identity", "tool_pair", "future", "worker", "retry", "foreign_protocol", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			h := nativeAgentTerminalHistory(t, "completed")[:5]
			now := h[len(h)-1].Time.Add(time.Second)
			switch mode {
			case "identity":
				h[1].Data.ModelID = "other"
			case "tool_pair":
				h[4].Data.ToolCallID = "other"
			case "future":
				h[1].Time = now.Add(time.Second)
			case "worker":
				h[0].WorkerID = "worker"
			case "retry":
				h[0].Data.RetryOfTaskID = "other"
			case "foreign_protocol":
				h[0].Data.Harness.Protocol = ""
			}
			histories := [][]runtime.Event{h}
			if mode == "multiple" {
				histories = append(histories, h)
			}
			if p, e := PlanInterruptedNativeAgent(histories, now, false); e == nil || len(p.Events) != 0 {
				t.Fatal("accepted ambiguous history", p, e)
			}
		})
	}
}
