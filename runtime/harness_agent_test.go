package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/harness/toolbridge"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type nativeAgentTools struct {
	invoke func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)
}

func (n nativeAgentTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped invocation")
}
func (n nativeAgentTools) ExecuteScoped(c context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
	return n.invoke(c, x)
}
func (n nativeAgentTools) ToolBehavior(string) runtime.ToolBehavior { return runtime.BehaviorReadOnly }
func agentRequest(tools nativeAgentTools) runtime.HarnessAgentRequest {
	var unused atomic.Int32
	q := nativeRequest(&unused)
	q.Execute = nil
	return runtime.HarnessAgentRequest{Request: q, MaxTurns: 3, Tools: tools}
}
func agentProposal(ctx context.Context, s *runtime.HarnessAgentSession, r runtime.HarnessAgentRequest) (runtime.ToolExecution, error) {
	turn, attempt, err := s.BeginTurn(ctx)
	if err != nil {
		return runtime.ToolExecution{}, err
	}
	calls, err := s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: r.Request.Attribution.Identity, Calls: []providers.ToolCall{{ID: "native-call", Name: "lookup", Arguments: []byte(`{"path":"safe"}`)}}, Usage: &providers.Usage{InputTokens: 20, OutputTokens: 5}})
	if err != nil {
		return runtime.ToolExecution{}, err
	}
	return calls[0], nil
}
func agentFinal(ctx context.Context, s *runtime.HarnessAgentSession, r runtime.HarnessAgentRequest) (runtime.HarnessOutput, error) {
	turn, attempt, err := s.BeginTurn(ctx)
	if err != nil {
		return runtime.HarnessOutput{}, err
	}
	_, err = s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: r.Request.Attribution.Identity, Text: "secret final", Usage: &providers.Usage{InputTokens: 30, OutputTokens: 7}})
	return runtime.HarnessOutput{Actual: r.Request.Attribution.Identity, Text: "secret final"}, err
}
func TestHarnessAgentBridgeDurableProposalAndResult(t *testing.T) {
	db, _ := store(t)
	ctx := context.Background()
	var calls atomic.Int32
	tools := nativeAgentTools{invoke: func(c context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		calls.Add(1)
		events, err := db.Read(c, x.TaskID, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 4 || events[3].Kind != runtime.ToolStarted || events[2].Kind != runtime.TurnCompleted || events[3].TurnID != x.TurnID || events[3].AttemptID != x.AttemptID || string(x.Call.Arguments) != `{"path":"safe"}` {
			t.Error("effect before durable bound proposal")
		}
		return runtime.ToolResult{Content: "secret tool", Effect: runtime.NoEffect}, nil
	}}
	r := agentRequest(tools)
	r.Request.OutputView = func(s string) string { return strings.ReplaceAll(s, "secret", "[REDACTED]") }
	r.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		x, err := agentProposal(ctx, s, r)
		if err != nil {
			return runtime.HarnessOutput{}, err
		}
		b, err := toolbridge.New(ctx, 2, time.Second, s.Invoke)
		if err != nil {
			return runtime.HarnessOutput{}, err
		}
		defer b.Close()
		if err = b.Register(x); err != nil {
			return runtime.HarnessOutput{}, err
		}
		for range 2 {
			req := httptest.NewRequest("POST", "http://localhost/v1/tool", strings.NewReader(`{"call_id":"native-call"}`))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer "+b.Token())
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			b.ServeHTTP(w, req)
			if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), "[REDACTED] tool") {
				t.Errorf("unsafe result %d %s", w.Code, w.Body.String())
			}
		}
		return agentFinal(ctx, s, r)
	}
	out, text, err := runtime.RunHarnessAgent(ctx, db, r)
	if err != nil || text != "[REDACTED] final" || out.Status != "completed" || calls.Load() != 1 {
		t.Fatal(out, text, err, calls.Load())
	}
	events, err := db.Read(ctx, r.Request.TaskID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted, runtime.ToolCompleted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted}
	if len(events) != len(kinds) {
		t.Fatal(len(events))
	}
	for i, e := range events {
		if e.Kind != kinds[i] || e.Sequence != int64(i+1) || e.Data.Accepted != nil {
			t.Fatalf("bad event %d %+v", i, e)
		}
	}
	if events[0].Data.Harness.Protocol != runtime.HarnessAgentProtocol || events[7].Data.Usage != nil || events[2].Data.Usage.InputTokens != 20 || events[6].Data.Usage.InputTokens != 30 {
		t.Fatal("protocol or usage attribution lost")
	}
	totals, totalsErr := db.UsageTotals(ctx, accounting.Scope{TaskID: r.Request.TaskID})
	if totalsErr != nil || totals.Primary.Records != 1 || totals.Primary.InputTokens == nil || *totals.Primary.InputTokens != 50 || totals.Primary.OutputTokens == nil || *totals.Primary.OutputTokens != 12 {
		t.Fatal("native turn accounting", totals, totalsErr)
	}
	usage, replayErr := runtime.ValidateHarnessAgentJournal(events, r.Request.TaskID)
	if replayErr != nil || usage == nil || usage.InputTokens != 50 || usage.OutputTokens != 12 {
		t.Fatal("native journal replay", usage, replayErr)
	}
	for _, mode := range []string{"wrong_turn", "wrong_tool", "duplicate_id", "effect", "hidden_turn", "aggregate", "output", "missing_start"} {
		modified := make([]runtime.Event, len(events))
		for i, e := range events {
			modified[i], _ = e.Clone()
		}
		switch mode {
		case "wrong_turn":
			modified[4].TurnID = "other"
		case "wrong_tool":
			modified[4].Data.ToolCallID = "other"
		case "duplicate_id":
			modified[4].ID = modified[3].ID
		case "effect":
			modified[4].Data.Effect = runtime.UncertainEffect
		case "hidden_turn":
			modified[6].Data.ProviderID = "other"
		case "aggregate":
			modified[7].Data.Usage = &providers.Usage{InputTokens: 50, OutputTokens: 12}
		case "output":
			modified[6].Data.Text = "different"
		case "missing_start":
			modified[3].Kind = runtime.ToolCompleted
		}
		if _, err := runtime.ValidateHarnessAgentJournal(modified, r.Request.TaskID); err == nil {
			t.Errorf("accepted corrupt journal %s", mode)
		}
	}
	// Legacy ingestion must fail closed rather than silently treating a native
	// agent as a two-event completion before the new projector is implemented.
	if _, err = runtime.ValidateHarnessOutcome(events, r.Request.TaskID); err == nil {
		t.Fatal("legacy reader accepted new protocol")
	}
	if _, _, err = runtime.RunHarnessAgent(ctx, db, r); !errors.Is(err, runtime.ErrPersistence) || calls.Load() != 1 {
		t.Fatal("duplicate run executed", err)
	}
}
func TestHarnessAgentAmbiguousJournalNeverReplays(t *testing.T) {
	for _, failAt := range []int64{0, 2, 3, 4, 7} {
		for _, afterCommit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", failAt, afterCommit), func(t *testing.T) {
				db, _ := store(t)
				var calls atomic.Int32
				tools := nativeAgentTools{invoke: func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
					calls.Add(1)
					return runtime.ToolResult{Content: "result", Effect: runtime.NoEffect}, nil
				}}
				r := agentRequest(tools)
				r.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
					x, err := agentProposal(ctx, s, r)
					if err == nil {
						_, _ = s.Invoke(ctx, x)
					}
					// Deliberately swallow failures: the runtime must retain the first failure
					// and prevent any subsequent provider turn or successful terminal.
					out, _ := agentFinal(ctx, s, r)
					return out, nil
				}
				appends := 0
				j := journal(func(ctx context.Context, seq int64, e runtime.Event) error {
					appends++
					if seq == failAt && !afterCommit {
						return errors.New("disk")
					}
					err := db.Append(ctx, seq, e)
					if err != nil {
						return err
					}
					if seq == failAt {
						return errors.New("lost acknowledgement")
					}
					return nil
				})
				out, text, err := runtime.RunHarnessAgent(context.Background(), j, r)
				if !errors.Is(err, runtime.ErrPersistence) || text != "" || out.ID != "" || appends != int(failAt+1) {
					t.Fatal("continued after ambiguous append", out, text, err, appends)
				}
				expected := int32(0)
				if failAt >= 4 {
					expected = 1
				}
				if calls.Load() != expected {
					t.Fatal("unexpected effects", calls.Load())
				}
			})
		}
	}
}
func TestHarnessAgentInvalidProposalCannotExecute(t *testing.T) {
	for _, mode := range []string{"changed_args", "changed_task", "duplicate", "unanswered", "wrong_model", "changed_output", "turn_limit"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := store(t)
			var calls atomic.Int32
			r := agentRequest(nativeAgentTools{invoke: func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				calls.Add(1)
				return runtime.ToolResult{Effect: runtime.NoEffect}, nil
			}})
			if mode == "turn_limit" {
				r.MaxTurns = 1
			}
			r.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				if mode == "wrong_model" {
					turn, attempt, _ := s.BeginTurn(ctx)
					actual := r.Request.Attribution.Identity
					actual.Model = "other"
					_, _ = s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: actual, Text: "forged"})
					return runtime.HarnessOutput{Actual: r.Request.Attribution.Identity, Text: "forged"}, nil
				}
				x, err := agentProposal(ctx, s, r)
				if err != nil {
					return runtime.HarnessOutput{}, err
				}
				switch mode {
				case "changed_args":
					x.Call.Arguments = []byte(`{"path":"elsewhere"}`)
				case "changed_task":
					x.TaskID = "elsewhere"
				case "unanswered":
					return agentFinal(ctx, s, r)
				}
				_, err = s.Invoke(ctx, x)
				if mode == "duplicate" {
					_, err = s.Invoke(ctx, x)
				}
				if err != nil {
					return runtime.HarnessOutput{}, err
				}
				out, err := agentFinal(ctx, s, r)
				if mode == "changed_output" {
					out.Text = "not the verified response"
				}
				return out, err
			}
			out, text, err := runtime.RunHarnessAgent(context.Background(), db, r)
			if err == nil || text != "" || out.ID != "" {
				t.Fatal("invalid native lifecycle accepted", out, text, err)
			}
			expected := int32(0)
			if mode == "duplicate" || mode == "changed_output" || mode == "turn_limit" {
				expected = 1
			}
			if calls.Load() != expected {
				t.Fatal("unexpected effects", calls.Load())
			}
			events, _ := db.Read(context.Background(), r.Request.TaskID, 0, 20)
			last := events[len(events)-1]
			if last.Kind != runtime.TaskFailed || last.Data.HarnessOutcome != nil {
				t.Fatal("fabricated success")
			}
		})
	}
}

func TestHarnessAgentEffectFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"panic", "uncertain", "canceled", "end_then_proposal", "recoverable"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := store(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			r := agentRequest(nativeAgentTools{invoke: func(c context.Context, _ runtime.ToolExecution) (runtime.ToolResult, error) {
				calls.Add(1)
				switch mode {
				case "panic":
					panic("private tool error")
				case "uncertain":
					return runtime.ToolResult{Content: "unaccepted", Effect: runtime.UncertainEffect}, errors.New("ambiguous")
				case "canceled":
					cancel()
					select {
					case <-c.Done():
					case <-time.After(time.Second):
						t.Error("run cancellation not inherited")
					}
					return runtime.ToolResult{Effect: runtime.NoEffect}, c.Err()
				case "end_then_proposal":
					return runtime.ToolResult{Effect: runtime.NoEffect, EndToolUse: true}, nil
				default:
					return runtime.ToolResult{Effect: runtime.NoEffect}, runtime.ErrToolArguments
				}
			}})
			r.Execute = func(c context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				x, err := agentProposal(c, s, r)
				if err != nil {
					return runtime.HarnessOutput{}, err
				}
				// Even a background request context must inherit task cancellation.
				result, err := s.Invoke(context.Background(), x)
				if mode == "recoverable" && (!result.Failed || !result.Recoverable || result.Effect != runtime.NoEffect) {
					t.Error("known no-effect schema failure not recoverable")
				}
				if err != nil {
					return runtime.HarnessOutput{}, err
				}
				if mode == "end_then_proposal" {
					turn, attempt, err := s.BeginTurn(c)
					if err != nil {
						return runtime.HarnessOutput{}, err
					}
					_, err = s.CompleteTurn(c, turn, attempt, runtime.HarnessTurnOutput{Actual: r.Request.Attribution.Identity, Calls: []providers.ToolCall{{ID: "after-end", Name: "lookup", Arguments: []byte(`{}`)}}})
					return runtime.HarnessOutput{}, err
				}
				return agentFinal(c, s, r)
			}
			out, _, err := runtime.RunHarnessAgent(ctx, db, r)
			if (err == nil) != (mode == "recoverable") || calls.Load() != 1 {
				t.Fatal("wrong effect disposition", out, err, calls.Load())
			}
			events, e := db.Read(context.Background(), r.Request.TaskID, 0, 20)
			if e != nil {
				t.Fatal(e)
			}
			if len(events) < 6 || events[3].Kind != runtime.ToolStarted || events[4].Kind != runtime.ToolCompleted {
				t.Fatal("missing durable effect boundaries")
			}
			if mode == "panic" && events[4].Data.Effect != runtime.UncertainEffect {
				t.Fatal("panic effect downgraded")
			}
			terminal := events[len(events)-1]
			if mode == "canceled" && terminal.Kind != runtime.TaskCanceled {
				t.Fatal("cancellation not terminal")
			}
			if mode != "recoverable" && (terminal.Kind == runtime.TaskCompleted || terminal.Data.HarnessOutcome != nil) {
				t.Fatal("failure gained quality evidence")
			}
		})
	}
}

func TestHarnessAgentUsageUnknownAndMeasuredZero(t *testing.T) {
	for _, mode := range []string{"unknown", "zero", "unfinished"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := store(t)
			r := agentRequest(nativeAgentTools{invoke: func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				t.Error("unexpected tool")
				return runtime.ToolResult{}, errors.New("unexpected")
			}})
			r.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				turn, attempt, err := s.BeginTurn(ctx)
				if err != nil {
					return runtime.HarnessOutput{}, err
				}
				if mode == "unfinished" {
					return runtime.HarnessOutput{}, errors.New("provider disconnected")
				}
				var usage *providers.Usage
				if mode == "zero" {
					usage = &providers.Usage{}
				}
				_, err = s.CompleteTurn(ctx, turn, attempt, runtime.HarnessTurnOutput{Actual: r.Request.Attribution.Identity, Text: "final", Usage: usage})
				return runtime.HarnessOutput{Actual: r.Request.Attribution.Identity, Text: "final"}, err
			}
			_, _, err := runtime.RunHarnessAgent(context.Background(), db, r)
			if (err != nil) != (mode == "unfinished") {
				t.Fatal(err)
			}
			totals, err := db.UsageTotals(context.Background(), accounting.Scope{TaskID: r.Request.TaskID})
			if err != nil || totals.Primary.Records != 1 {
				t.Fatal(totals, err)
			}
			if mode == "zero" {
				if totals.Primary.UnknownUsageRecords != 0 || totals.Primary.InputTokens == nil || *totals.Primary.InputTokens != 0 {
					t.Fatal("measured zero lost", totals)
				}
			} else if totals.Primary.UnknownUsageRecords != 1 {
				t.Fatal("unknown fabricated", totals)
			}
		})
	}
}
