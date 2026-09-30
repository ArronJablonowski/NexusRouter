package telemetry

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

type delegationCompactionProvider struct{}

func (delegationCompactionProvider) Models(context.Context) ([]string, error) {
	return []string{"fixture"}, nil
}
func (delegationCompactionProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "answer", Usage: &providers.Usage{InputTokens: 1, OutputTokens: 1}, Done: true, FinishReason: "stop"})
}

func delegationCompactionEvidenceFixture(t *testing.T, release bool) (*Store, []runtime.Event, []providers.Message, sessions.ContextCompactionPlanStart, runtime.ContextCompactionPlan) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "delegation.db"))
	if err != nil {
		t.Fatal(err)
	}
	points := []tools.PolicyPoint{{Tool: "delegate", Scope: "delegation"}, {Tool: "delegate_batch", Scope: "delegation"}, {Tool: "read_file", Scope: "workspace"}}
	parentSnapshot, err := tools.SealPolicySnapshot(&tools.Policy{Default: tools.Allow}, points)
	if err != nil {
		t.Fatal(err)
	}
	childSnapshot, err := tools.SealPolicySnapshot(&tools.Policy{Default: tools.Deny}, points)
	if err != nil {
		t.Fatal(err)
	}
	parentPolicy, _ := parentSnapshot.CanonicalJSON()
	childPolicy, _ := childSnapshot.CanonicalJSON()
	plan := runtime.ContextCompactionPlan{PlanDigest: strings.Repeat("a", 64), Engine: runtime.ContextEngineIdentity{Digest: strings.Repeat("b", 64)}}
	policy, _ := json.Marshal(struct {
		Delegation frozenDelegationCompactionPolicy `json:"delegation"`
	}{frozenDelegationCompactionPolicy{Version: 1, Enabled: true, Parent: parentSnapshot, Child: childSnapshot}})
	start := sessions.ContextCompactionPlanStart{PolicySnapshot: policy}
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: "parent", PlanDigest: plan.PlanDigest, InheritedEngineDigest: plan.Engine.Digest, Scope: "delegation-parent",
		ParentPolicy: parentPolicy, ChildPolicy: childPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	base := time.Unix(100, 0).UTC()
	workKinds := []runtime.Kind{runtime.TaskStarted, runtime.WorkerStarted, runtime.WorkerHeartbeat, runtime.EvaluationRecorded, runtime.WorkerCompleted, runtime.TaskCompleted}
	for i, kind := range workKinds {
		event := runtime.Event{Version: 1, ID: "work-event-" + string(rune('0'+i)), TaskID: "work", SessionID: "session", CorrelationID: "work",
			WorkerID: "worker", Sequence: int64(i + 1), Time: base.Add(time.Duration(i) * time.Second), Kind: kind}
		if i == 0 {
			event.Data = runtime.Data{ParentTaskID: "parent", DelegationOrigin: origin, DelegationCompaction: &authority}
		}
		if kind == runtime.EvaluationRecorded {
			accepted := true
			event.Data = runtime.Data{Accepted: &accepted, Code: "worker_validator"}
		}
		if kind == runtime.WorkerCompleted {
			event.Data.Text = "answer"
		}
		if err = store.Append(ctx, int64(i), event); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if i == 0 {
			_, leaseErr := store.AcquireLease(ctx, "work", "worker", authority.Scope, false, time.Now().UTC(), time.Minute)
			if leaseErr != nil {
				store.Close()
				t.Fatal(leaseErr)
			}
		}
	}
	loop := runtime.Loop{Journal: store, Provider: delegationCompactionProvider{}}
	_, err = loop.Run(ctx, runtime.RunRequest{TaskID: "execution", SessionID: "execution-session", ParentTaskID: "work", ProviderID: "fixture",
		RequireText: true, MaxTurns: 1, MaxOutputBytes: 1024,
		Inference: providers.Request{Model: "fixture", Messages: []providers.Message{{Role: "user", Content: "work"}}}})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if release {
		var token string
		if err = store.db.QueryRow(`SELECT token FROM resource_leases WHERE task_id='work'`).Scan(&token); err != nil || store.ReleaseLease(ctx, token, "worker") != nil {
			store.Close()
			t.Fatal("release worker lease", err)
		}
	}
	parentEvents := []runtime.Event{
		{Version: 1, ID: "tool-start", TaskID: "parent", SessionID: "session", Sequence: 2, Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "delegate"}},
		{Version: 1, ID: "tool-done", TaskID: "parent", SessionID: "session", Sequence: 3, Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect, Text: `{"work_task_id":"work","execution_task_id":"execution","untrusted_output":"answer"}`}},
	}
	suffix := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"work","validation":"text"}`)}}},
		{Role: "tool", ToolCallID: "call", Content: `{"work_task_id":"work","execution_task_id":"execution","untrusted_output":"answer"}`},
	}
	return store, parentEvents, suffix, start, plan
}

func TestDeriveDelegationCompactionBindingsRequiresReleasedExactTree(t *testing.T) {
	tests := []struct {
		name    string
		release bool
		mutate  func(*testing.T, *Store, *sessions.ContextCompactionPlanStart, *runtime.ContextCompactionPlan)
		valid   bool
	}{
		{name: "released", release: true, valid: true},
		{name: "held", release: false},
		{name: "canceled", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`INSERT INTO task_cancellations VALUES('work','cancel-request','2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parent canceled", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`INSERT INTO task_heads VALUES('parent','session',0,'running');
				INSERT INTO task_cancellations VALUES('parent','parent-cancel-request','2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "recovered", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`INSERT INTO lease_recoveries SELECT token,?,X'01' FROM resource_leases WHERE task_id='work'`, strings.Repeat("c", 64)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "plan drift", release: true, mutate: func(_ *testing.T, _ *Store, _ *sessions.ContextCompactionPlanStart, plan *runtime.ContextCompactionPlan) {
			plan.PlanDigest = strings.Repeat("d", 64)
		}},
		{name: "lease owner drift", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`UPDATE resource_leases SET owner='other' WHERE task_id='work'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "lease scope drift", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`UPDATE resource_leases SET scope='other' WHERE task_id='work'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "lease mode drift", release: true, mutate: func(t *testing.T, store *Store, _ *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			if _, err := store.db.Exec(`UPDATE resource_leases SET writer=1 WHERE task_id='work'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy drift", release: true, mutate: func(_ *testing.T, _ *Store, start *sessions.ContextCompactionPlanStart, _ *runtime.ContextCompactionPlan) {
			start.PolicySnapshot = json.RawMessage(`{"delegation":{"enabled":true,"parent":{"version":2},"child":{"version":1}}}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, parentEvents, suffix, start, plan := delegationCompactionEvidenceFixture(t, test.release)
			defer store.Close()
			if test.mutate != nil {
				test.mutate(t, store, &start, &plan)
			}
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			bindings, err := deriveDelegationCompactionBindings(context.Background(), tx, parentEvents, suffix, "parent", start, plan)
			if !test.valid {
				if err == nil {
					t.Fatal("invalid delegation evidence authorized compaction", bindings)
				}
				return
			}
			if err != nil || len(bindings) != 1 {
				t.Fatal(bindings, err)
			}
			binding := bindings[0]
			if binding.ParentTaskID != "parent" || binding.WorkTaskID != "work" || binding.ExecutionTaskID != "execution" ||
				binding.Origin.ToolCallID != "call" || binding.EngineDigest != plan.Engine.Digest || binding.Validate() != nil {
				t.Fatal("inexact delegation binding", binding)
			}
		})
	}
}

func TestCompletedDelegationCallsRequiresExactBatchCompletion(t *testing.T) {
	call := providers.ToolCall{ID: "batch", Name: "delegate_batch", Arguments: json.RawMessage(`{"tasks":[{"prompt":"a","validation":"text"},{"prompt":"b","validation":"text"}]}`)}
	result := `{"results":[{"untrusted_output":"a"},{"untrusted_output":"b"}]}`
	suffix := []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: result}}
	events := []runtime.Event{
		{Kind: runtime.ToolStarted, SessionID: "session", Sequence: 1, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name}},
		{Kind: runtime.ToolCompleted, SessionID: "session", Sequence: 2, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, Text: result, Effect: runtime.NoEffect}},
	}
	calls, err := completedDelegationCalls(suffix, events)
	if err != nil || len(calls) != 1 || len(calls[call.ID].results) != 2 ||
		calls[call.ID].results[0] != rawJSONDigest(json.RawMessage(`{"untrusted_output":"a"}`)) ||
		calls[call.ID].results[1] != rawJSONDigest(json.RawMessage(`{"untrusted_output":"b"}`)) {
		t.Fatal(calls, err)
	}
	if _, err = completedDelegationCalls(suffix[:1], events); err == nil {
		t.Fatal("batch without tool reply accepted")
	}
	events[1].Data.Code = "tool_failed"
	if _, err = completedDelegationCalls(suffix, events); err == nil {
		t.Fatal("failed batch completion accepted")
	}
}

func TestDelegationCompactionRejectsChildContextSubstitutionAndIgnoresOtherEpochs(t *testing.T) {
	store, parentEvents, suffix, start, plan := delegationCompactionEvidenceFixture(t, true)
	defer store.Close()
	ctx := context.Background()
	frozen, err := delegationCompactionPolicyEvidence(start.PolicySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	parentPolicy, _ := frozen.Parent.CanonicalJSON()
	childPolicy, _ := frozen.Child.CanonicalJSON()
	historicalAuthority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: "parent", PlanDigest: plan.PlanDigest, InheritedEngineDigest: plan.Engine.Digest, Scope: "delegation-parent",
		ParentPolicy: parentPolicy, ChildPolicy: childPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	historical := runtime.Event{Version: 1, ID: "historical-start", TaskID: "historical-work", SessionID: "session", CorrelationID: "historical-work",
		WorkerID: "historical-worker", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: "parent", DelegationOrigin: &runtime.DelegationOrigin{Version: 1, TurnID: "old-turn", AttemptID: "old-attempt", ToolCallID: "old-call", ToolName: "delegate"}, DelegationCompaction: &historicalAuthority}}
	if err = store.Append(ctx, 0, historical); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bindings, err := deriveDelegationCompactionBindings(ctx, tx, parentEvents, suffix, "parent", start, plan)
	if err != nil || len(bindings) != 1 || bindings[0].WorkTaskID != "work" {
		t.Fatal("unrelated epoch contaminated activation", bindings, err)
	}
	badSuffix := append([]providers.Message(nil), suffix...)
	badSuffix[0] = suffix[0]
	badSuffix[0].ToolCalls = append([]providers.ToolCall(nil), suffix[0].ToolCalls...)
	badSuffix[0].ToolCalls[0].Arguments = json.RawMessage(`{"prompt":"substituted","validation":"text"}`)
	if bindings, err = deriveDelegationCompactionBindings(ctx, tx, parentEvents, badSuffix, "parent", start, plan); err == nil {
		t.Fatal("substituted child context accepted", bindings)
	}
}
