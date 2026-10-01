package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type nativeAgentRecoveryTools struct{ calls *int }

func (nativeAgentRecoveryTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped")
}
func (t nativeAgentRecoveryTools) ExecuteScoped(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
	*t.calls++
	return runtime.ToolResult{Content: "confirmed fixture effect", Effect: runtime.ConfirmedEffect}, nil
}
func (nativeAgentRecoveryTools) ToolBehavior(string) runtime.ToolBehavior {
	return runtime.BehaviorNonIdempotentWrite
}

type nativeAgentRecoveryJournal struct {
	runtime.Journal
	failAt int64
}

func (j nativeAgentRecoveryJournal) Append(ctx context.Context, seq int64, e runtime.Event) error {
	if seq == j.failAt {
		return errors.New("lost native owner")
	}
	return j.Journal.Append(ctx, seq, e)
}

func TestNativeAgentDispatcherRecoveryNeverRepeatsEffects(t *testing.T) {
	for _, mode := range []string{"completed", "before_tool", "uncertain_effect", "after_effect", "after_final", "canceled", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			svc, db, providerCalls := recoveryFixture(t)
			claim := recoveryClaim(t, svc, db)
			failAt := int64(-1)
			switch mode {
			case "before_tool":
				failAt = 3
			case "uncertain_effect", "canceled", "concurrent":
				failAt = 4
			case "after_effect":
				failAt = 5
			case "after_final":
				failAt = 7
			}
			identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "agent-v1", Provider: "local", Model: "fixture", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}
			effects, runs := 0, 0
			journal := nativeAgentRecoveryJournal{redactingJournal{db: db, submissionID: claim.Status.ID, submissionToken: claim.Token}, failAt}
			request := runtime.HarnessAgentRequest{Request: runtime.HarnessRequest{TaskID: "agent-recovery", SessionID: "agent-recovery", SubmissionID: claim.Status.ID, Messages: []providers.Message{{Role: "user", Content: "fixture"}}, Privacy: "local_only", ContextTokens: 8192, MaxOutputBytes: 4096, Attribution: runtime.HarnessAttribution{Identity: identity, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "hard"}}}, MaxTurns: 2, Tools: nativeAgentRecoveryTools{&effects}}
			request.Execute = func(c context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				runs++
				turn, attempt, e := s.BeginTurn(c)
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				calls, e := s.CompleteTurn(c, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Calls: []providers.ToolCall{{ID: "call", Name: "write", Arguments: []byte(`{}`)}}, Usage: &providers.Usage{InputTokens: 10, OutputTokens: 4}})
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				if _, e = s.Invoke(c, calls[0]); e != nil {
					return runtime.HarnessOutput{}, e
				}
				turn, attempt, e = s.BeginTurn(c)
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				_, e = s.CompleteTurn(c, turn, attempt, runtime.HarnessTurnOutput{Actual: identity, Text: "durable answer", Usage: &providers.Usage{InputTokens: 20, OutputTokens: 4}})
				return runtime.HarnessOutput{Actual: identity, Text: "durable answer"}, e
			}
			_, _, err := runtime.RunHarnessAgent(ctx, journal, request)
			if (err == nil) != (mode == "completed") {
				t.Fatal(mode, err)
			}
			before, err := db.Read(ctx, "agent-recovery", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			beforeSnapshot, err := sessions.Replay(ctx, db, "agent-recovery")
			if err != nil {
				t.Fatal(err)
			}
			if c, e := db.RecoverInterruptedNativeAgentCommitScreened(ctx, claim.Status.ID, svc.submissionConfigDigest(), time.Now().UTC(), nil); e != nil || c.Changed {
				t.Fatal("live owner recovered", c, e)
			}
			if mode == "canceled" {
				if _, e := svc.CancelSubmission(ctx, claim.Status.ID); e != nil {
					t.Fatal(e)
				}
			}
			expireRecoveryClaim(t, svc, claim.Status.ID)
			if c, e := db.RecoverInterruptedNativeAgentCommitScreened(ctx, claim.Status.ID, strings.Repeat("0", 64), time.Now().UTC(), nil); e != nil || c.Changed {
				t.Fatal("wrong configuration recovered", c, e)
			}
			if mode == "concurrent" {
				type result struct {
					changed bool
					err     error
				}
				results := make(chan result, 2)
				for range 2 {
					go func() {
						c, e := db.RecoverInterruptedNativeAgentCommitScreened(ctx, claim.Status.ID, svc.submissionConfigDigest(), time.Now().UTC(), nil)
						results <- result{c.Changed, e}
					}()
				}
				changed := 0
				for range 2 {
					r := <-results
					if r.err != nil {
						t.Fatal(r.err)
					}
					if r.changed {
						changed++
					}
				}
				if changed != 1 {
					t.Fatal("concurrent recovery did not commit exactly once", changed)
				}
			}
			dispatcher, err := StartDispatcher(ctx, svc)
			if err != nil {
				t.Fatal(err)
			}
			defer dispatcher.Close()
			want := "failed"
			if mode == "completed" {
				want = "succeeded"
			}
			if mode == "canceled" {
				want = "canceled"
			}
			status := awaitSubmission(t, ctx, svc, claim.Status.ID, want)
			if err = dispatcher.Close(); err != nil {
				t.Fatal(err)
			}
			expectedEffects := 1
			if mode == "before_tool" {
				expectedEffects = 0
			}
			if effects != expectedEffects || runs != 1 || providerCalls.Load() != 0 || status.Result == nil {
				t.Fatal("recovery repeated execution", status, effects, runs, providerCalls.Load())
			}
			after, err := db.Read(ctx, "agent-recovery", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "completed" {
				if !reflect.DeepEqual(before, after) || status.Result.Text != "durable answer" || status.Result.Turns != 2 || status.Result.Usage == nil || status.Result.Usage.InputTokens != 30 {
					t.Fatal("lost committed completion", status)
				}
			} else {
				if len(after) != len(before)+1 || !reflect.DeepEqual(before, after[:len(before)]) || status.Result.Text != "" || after[len(after)-1].Data.HarnessOutcome != nil {
					t.Fatal("rewrote effects or accepted uncommitted result", status)
				}
				snapshot, e := sessions.Replay(ctx, db, "agent-recovery")
				if e != nil || !reflect.DeepEqual(snapshot.Pending, beforeSnapshot.Pending) || snapshot.UncertainEffects != beforeSnapshot.UncertainEffects {
					t.Fatal("lost effect uncertainty", snapshot, e)
				}
				if _, e := runtime.ValidateHarnessOutcome(after, "agent-recovery"); e == nil {
					t.Fatal("recovery fabricated quality evidence")
				}
			}
			if mode != "completed" {
				if _, e := db.ResumeSource(ctx, "agent-recovery"); e == nil {
					t.Fatal("interruption granted replay authority")
				}
			}
			stale, _ := after[len(after)-1].Clone()
			stale.ID = "stale-native-owner"
			stale.Sequence++
			if e := journal.Journal.Append(ctx, stale.Sequence-1, stale); e == nil {
				t.Fatal("old owner appended after recovery")
			}
			receipts, err := db.RecoveryHistory(ctx, claim.Status.ID)
			if err != nil || len(receipts) != 1 {
				t.Fatal(receipts, err)
			}
			reason := "interrupted_native_agent"
			if mode == "completed" {
				reason = "terminal_history"
			}
			if receipts[0].Reason != reason {
				t.Fatal(receipts)
			}
			totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: "agent-recovery"})
			if err != nil || totals.Primary.Records != 1 {
				t.Fatal("missing atomic recovered usage", totals, err)
			}
			if c, e := db.RecoverInterruptedNativeAgentCommitScreened(ctx, claim.Status.ID, svc.submissionConfigDigest(), time.Now().UTC(), nil); e != nil || c.Changed {
				t.Fatal("duplicate native recovery", c, e)
			}
		})
	}
}
