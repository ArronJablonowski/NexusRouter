package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func dropContextCompactionDelegation52(db *sql.DB) error {
	_, err := db.Exec(`DROP TRIGGER context_compaction_delegation_binding;
		DROP TRIGGER context_compaction_delegation_immutable_delete;
		DROP TRIGGER context_compaction_delegation_immutable_update;
		DROP INDEX context_compaction_delegations_events;
		DROP INDEX context_compaction_delegations_tasks;
		DROP INDEX context_compaction_delegations_operation;
		DROP TABLE context_compaction_delegations;
		PRAGMA user_version=51;`)
	return err
}

func TestContextCompactionDelegationMigrationFresh51PreservationAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delegations.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('legacy','session',1,'completed')`); err != nil {
		t.Fatal(err)
	}
	if err = dropContextCompactionDelegation52(store.db); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var version, legacy, tables, indexes, triggers int
		err = store.db.QueryRow(`SELECT
			(SELECT user_version FROM pragma_user_version),
			(SELECT count(*) FROM task_heads WHERE task_id='legacy'),
			(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_compaction_delegations'),
			(SELECT count(*) FROM sqlite_master WHERE type='index' AND name GLOB 'context_compaction_delegations_*'),
			(SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name GLOB 'context_compaction_delegation_*')`).
			Scan(&version, &legacy, &tables, &indexes, &triggers)
		if err != nil || version != stateschema.Current || legacy != 1 || tables != 1 || indexes != 3 || triggers != 3 {
			t.Fatalf("attempt=%d version=%d legacy=%d objects=%d/%d/%d err=%v", attempt, version, legacy, tables, indexes, triggers, err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContextCompactionDelegationMigrationRejectsSchemaTamper(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tamper.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_delegation_immutable_update;
		CREATE TRIGGER context_compaction_delegation_immutable_update BEFORE UPDATE ON context_compaction_delegations BEGIN SELECT 1; END;`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("tampered schema reopened")
	}
}

func TestContextCompactionDelegationCrossDirectionMissingAndOrphan(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "bindings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, _ := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "binding-task")
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatal(err)
	}
	var factID, activationDigest string
	if err = store.db.QueryRow(`SELECT fact_id,activation_digest FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated'`, plan.OperationID).Scan(&factID, &activationDigest); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	binding, err := sessions.SealDelegationCompactionBinding(sessions.DelegationCompactionBinding{
		ParentTaskID: event.TaskID, WorkTaskID: "work-task", ExecutionTaskID: "execution-task", WorkerID: "worker", Scope: "delegation-" + event.TaskID,
		Origin:           runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"},
		WorkStartEventID: "work-start", WorkStartEventDigest: digest, WorkTerminalEventID: "work-end", WorkTerminalEventDigest: digest,
		ExecutionStartEventID: "execution-start", ExecutionStartEventDigest: digest, ExecutionContextDigest: digest,
		ExecutionTerminalEventID: "execution-end", ExecutionTerminalEventDigest: digest, PlanDigest: digest, AuthorityDigest: digest, EngineDigest: digest,
		ParentPolicyDigest: digest, ChildPolicyDigest: digest, ResultDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := json.Marshal([]sessions.DelegationCompactionBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_plan_fact_immutable_update;
		UPDATE context_compaction_plan_facts SET body=json_set(body,'$.activation.delegations',json(?)) WHERE fact_id=?`, bindings, factID); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateContextCompactionDelegationBindings(ctx, conn); err == nil {
		conn.Close()
		t.Fatal("missing normalized delegation accepted")
	}
	conn.Close()
	if _, err = store.db.Exec(`UPDATE context_compaction_plan_facts SET body=json_remove(body,'$.activation.delegations') WHERE fact_id=?;
		DROP TRIGGER context_compaction_delegation_binding;
		INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('work-task','s',1,'completed'),('execution-task','s',1,'completed');
		INSERT INTO context_compaction_delegations(
		activation_digest,ordinal,operation_id,fact_id,version,parent_task_id,work_task_id,execution_task_id,worker_id,scope,
		origin_version,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,
		work_start_event_id,work_start_event_digest,work_terminal_event_id,work_terminal_event_digest,
		execution_start_event_id,execution_start_event_digest,execution_context_digest,execution_terminal_event_id,execution_terminal_event_digest,
		plan_digest,authority_digest,engine_digest,parent_policy_digest,child_policy_digest,result_digest,binding_digest,body)
		VALUES(?,0,?,?,1,?,'work-task','execution-task','worker',?,1,'turn','attempt','call','delegate',
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, activationDigest, plan.OperationID, factID, event.TaskID, "delegation-"+event.TaskID,
		event.ID, digest, event.ID, digest, event.ID, digest, digest, event.ID, digest, binding.PlanDigest, binding.AuthorityDigest,
		digest, digest, digest, digest, binding.BindingDigest, mustJSON(t, binding)); err != nil {
		t.Fatal(err)
	}
	conn, err = store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = validateContextCompactionDelegationBindings(ctx, conn); err == nil {
		t.Fatal("orphan normalized delegation accepted")
	}
}

func TestContextCompactionDelegationDowngradeRefusesNonEmptyFuture(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "downgrade.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "downgrade-task")
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatal(err)
	}
	var factID, activationDigest string
	if err = store.db.QueryRow(`SELECT fact_id,activation_digest FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated'`, plan.OperationID).Scan(&factID, &activationDigest); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_delegation_binding;
		INSERT INTO context_compaction_delegations(
		activation_digest,ordinal,operation_id,fact_id,version,parent_task_id,work_task_id,execution_task_id,worker_id,scope,
		origin_version,origin_turn_id,origin_attempt_id,origin_tool_call_id,origin_tool_name,
		work_start_event_id,work_start_event_digest,work_terminal_event_id,work_terminal_event_digest,
		execution_start_event_id,execution_start_event_digest,execution_context_digest,execution_terminal_event_id,execution_terminal_event_digest,
		plan_digest,authority_digest,engine_digest,parent_policy_digest,child_policy_digest,result_digest,binding_digest,body)
		VALUES(?,0,?,?,1,?,?,?,'worker',?,1,'turn','attempt','call','delegate',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,X'7B7D');
		PRAGMA user_version=51;`, activationDigest, plan.OperationID, factID, event.TaskID, event.TaskID, event.TaskID, "delegation-"+event.TaskID,
		event.ID, digest, event.ID, digest, event.ID, digest, digest, event.ID, digest, digest, digest, digest, digest, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("non-empty future delegation schema was discarded")
	}
}

func TestContextCompactionDelegationReopenRederivesContextAndResultDigests(t *testing.T) {
	for name, mutate := range map[string]func(*sessions.DelegationCompactionBinding){
		"execution context": func(binding *sessions.DelegationCompactionBinding) {
			binding.ExecutionContextDigest = strings.Repeat("e", 64)
		},
		"result": func(binding *sessions.DelegationCompactionBinding) {
			binding.ResultDigest = strings.Repeat("f", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "durable-projection.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			appendActivatedDelegationCompactionFixture(t, store)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, path)
			if err != nil {
				t.Fatal("valid durable delegation did not reopen", err)
			}
			corruptDelegationBindingAndReseal(t, store.db, mutate)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, openErr := Open(ctx, path); openErr == nil {
				reopened.Close()
				t.Fatal("delegation digest not derived from durable histories")
			}
		})
	}
}

func TestContextCompactionDelegationReopenRejectsParentPromptAndSharedChildSession(t *testing.T) {
	for name, corrupt := range map[string]func(*testing.T, *sql.DB){
		"parent prompt": func(t *testing.T, db *sql.DB) {
			mutateDurableEvent(t, db, "parent-proposal", func(event *runtime.Event) {
				event.Data.ToolCalls[0].Arguments = json.RawMessage(`{"prompt":"other","validation":"text"}`)
			})
		},
		"shared child session": func(t *testing.T, db *sql.DB) {
			rows, err := db.Query(`SELECT id FROM events WHERE task_id='execution' ORDER BY sequence`)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				mutateDurableEvent(t, db, id, func(event *runtime.Event) { event.SessionID = "session" })
			}
			if _, err = db.Exec(`UPDATE task_heads SET session_id='session' WHERE task_id='execution'`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "parent-projection.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			appendActivatedDelegationCompactionFixture(t, store)
			corrupt(t, store.db)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, openErr := Open(ctx, path); openErr == nil {
				reopened.Close()
				t.Fatal("parent prompt/session isolation corruption reopened")
			}
		})
	}
}

func appendActivatedDelegationCompactionFixture(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	points := []tools.PolicyPoint{{Tool: "delegate", Scope: "delegation"}, {Tool: "delegate_batch", Scope: "delegation"}, {Tool: "read_file", Scope: "workspace"}}
	parentSnapshot, err := tools.SealPolicySnapshot(&tools.Policy{Default: tools.Allow}, points)
	if err != nil {
		t.Fatal(err)
	}
	childSnapshot, err := tools.SealPolicySnapshot(&tools.Policy{Default: tools.Deny}, points)
	if err != nil {
		t.Fatal(err)
	}
	start, draft := compactionPlanStartFixture(t, store)
	start.PolicySnapshot, err = json.Marshal(struct {
		Delegation frozenDelegationCompactionPolicy `json:"delegation"`
	}{frozenDelegationCompactionPolicy{Version: 1, Enabled: true, Parent: parentSnapshot, Child: childSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	start, err = sessions.SealContextCompactionPlanStart(start)
	if err != nil {
		t.Fatal(err)
	}
	plan := approveDelegationCompactionPlanFixture(t, store, start, draft)
	appendCompletedDelegationFixture(t, store, plan, parentSnapshot, childSnapshot)

	base := time.Unix(1000, 0).UTC()
	call := providers.ToolCall{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"work","validation":"text"}`)}
	result := `{"work_task_id":"work","execution_task_id":"execution","untrusted_output":"answer"}`
	parent := []runtime.Event{
		{Version: 1, ID: "parent-start", TaskID: "parent", SessionID: "session", CorrelationID: "parent", Sequence: 1, Time: base, Kind: runtime.TaskStarted,
			Data: runtime.Data{ParentTaskID: plan.Compaction.SourceTaskID, Messages: append([]providers.Message(nil), plan.OriginalPrefix...)}},
		{Version: 1, ID: "parent-turn", TaskID: "parent", SessionID: "session", CorrelationID: "parent", Sequence: 2, Time: base.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"},
		{Version: 1, ID: "parent-proposal", TaskID: "parent", SessionID: "session", CorrelationID: "parent", Sequence: 3, Time: base.Add(2 * time.Second), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{call}, FinishReason: "tool_calls"}},
		{Version: 1, ID: "parent-tool-start", TaskID: "parent", SessionID: "session", CorrelationID: "parent", Sequence: 4, Time: base.Add(3 * time.Second), Kind: runtime.ToolStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect}},
		{Version: 1, ID: "parent-tool-done", TaskID: "parent", SessionID: "session", CorrelationID: "parent", Sequence: 5, Time: base.Add(4 * time.Second), Kind: runtime.ToolCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect, Text: result}},
	}
	for _, event := range parent {
		if err = store.Append(ctx, event.Sequence-1, event); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.TaskSnapshot(ctx, "parent")
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := runtime.ExtendContextLineage(current.ContextLineage, "parent", 6, plan.Compaction, current.Messages)
	if err != nil {
		t.Fatal(err)
	}
	compact := runtime.Event{Version: 1, ID: "parent-compact", TaskID: "parent", SessionID: "session", CorrelationID: "parent",
		Sequence: 6, Time: base.Add(5 * time.Second), Kind: runtime.ContextCompacted,
		Data: runtime.Data{Compaction: plan.Compaction, ContextLineage: lineage, ParentTaskID: plan.Compaction.SourceTaskID,
			Messages: append([]providers.Message(nil), plan.ReplacementPrefix...), ReplacedMessages: len(plan.OriginalPrefix)}}
	if err = store.AppendContextCompaction(ctx, 5, compact, plan); err != nil {
		t.Fatal(err)
	}
}

func approveDelegationCompactionPlanFixture(t *testing.T, store *Store, start sessions.ContextCompactionPlanStart, draft *sessions.SummaryDraft) runtime.ContextCompactionPlan {
	t.Helper()
	ctx := context.Background()
	state, _, err := store.BeginContextCompactionPlan(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	attempt := summaryAttemptForCompactionStart(start)
	attempt.Status, attempt.Draft, attempt.FinishedAt = "drafted", draft, start.StartedAt.Add(time.Second)
	if err = store.CompleteSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	draftDigest, err := sessions.SummaryDraftDigest(*draft)
	if err != nil {
		t.Fatal(err)
	}
	review := sessions.SummaryReview{Version: 2, ID: "delegation-review", AttemptID: start.AttemptID, Decision: "approved",
		Note: "deterministic validation passed", ValidatorID: "project-tests-v1", SourceSequence: start.SourceSequence,
		SourceDigest: start.SourceDigest, DraftDigest: draftDigest, Time: start.StartedAt.Add(2 * time.Second)}
	if err = store.RecordSummaryReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	source, err := store.TaskSnapshot(ctx, start.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, checkpoint, err := sessions.PrepareContinuation(source, draft.Request)
	if err != nil {
		t.Fatal(err)
	}
	tail := []providers.Message{{Role: "user", Content: "volatile request"}}
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = start.AttemptID, review.ID
	plan, err := runtime.SealContextCompactionPlan(runtime.ContextCompactionPlan{
		OperationID: start.OperationID, OperationDigest: start.OperationDigest, RequestID: start.RequestID,
		RequestDigest: start.RequestDigest, Compaction: checkpoint, ConfigDigest: start.ConfigDigest, PolicyDigest: start.PolicyDigest,
		Engine: start.Engine, Tiers: start.Tiers, OriginalPrefix: append(append([]providers.Message{}, source.Messages...), tail...),
		ReplacementPrefix: append(replacement, tail...), LiveSuffixBoundary: len(source.Messages) + len(tail), DraftDigest: draftDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := compactionPlanFactFixture(t, start, state.Facts[0], sessions.ContextCompactionPrepared, &plan, 2)
	state, err = store.PrepareContextCompactionPlan(ctx, plan, prepared)
	if err != nil {
		t.Fatal(err)
	}
	validated := compactionPlanFactFixture(t, start, prepared, sessions.ContextCompactionValidated, &plan, 3)
	state, err = store.ValidateContextCompactionPlan(ctx, validated)
	if err != nil {
		t.Fatal(err)
	}
	approved := compactionPlanFactFixture(t, start, validated, sessions.ContextCompactionApproved, &plan, 4)
	if _, err = store.ApproveContextCompactionPlan(ctx, approved); err != nil {
		t.Fatal(err)
	}
	return plan
}

func appendCompletedDelegationFixture(t *testing.T, store *Store, plan runtime.ContextCompactionPlan, parentSnapshot, childSnapshot tools.PolicySnapshot) {
	t.Helper()
	ctx := context.Background()
	parentPolicy, _ := parentSnapshot.CanonicalJSON()
	childPolicy, _ := childSnapshot.CanonicalJSON()
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: "parent", PlanDigest: plan.PlanDigest, InheritedEngineDigest: plan.Engine.Digest, Scope: "delegation-parent",
		ParentPolicy: parentPolicy, ChildPolicy: childPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	base := time.Unix(100, 0).UTC()
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.WorkerStarted, runtime.WorkerHeartbeat, runtime.EvaluationRecorded, runtime.WorkerCompleted, runtime.TaskCompleted}
	for i, kind := range kinds {
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
			t.Fatal(err)
		}
		if i == 0 {
			if _, err = store.AcquireLease(ctx, "work", "worker", authority.Scope, false, time.Now().UTC(), time.Minute); err != nil {
				t.Fatal(err)
			}
		}
	}
	loop := runtime.Loop{Journal: store, Provider: delegationCompactionProvider{}}
	if _, err = loop.Run(ctx, runtime.RunRequest{TaskID: "execution", SessionID: "child-session", ParentTaskID: "work", ProviderID: "fixture",
		RequireText: true, MaxTurns: 1, MaxOutputBytes: 1024,
		Inference: providers.Request{Model: "fixture", Messages: []providers.Message{{Role: "user", Content: "work"}}}}); err != nil {
		t.Fatal(err)
	}
	var token string
	if err = store.db.QueryRow(`SELECT token FROM resource_leases WHERE task_id='work'`).Scan(&token); err != nil || store.ReleaseLease(ctx, token, "worker") != nil {
		t.Fatal("release worker lease", err)
	}
}

func corruptDelegationBindingAndReseal(t *testing.T, db *sql.DB, mutate func(*sessions.DelegationCompactionBinding)) {
	t.Helper()
	var factBody []byte
	if err := db.QueryRow(`SELECT body FROM context_compaction_plan_facts WHERE kind='activated'`).Scan(&factBody); err != nil {
		t.Fatal(err)
	}
	var fact sessions.ContextCompactionLifecycleFact
	if json.Unmarshal(factBody, &fact) != nil || fact.Activation == nil || len(fact.Activation.Delegations) != 1 {
		t.Fatal("invalid activation fixture")
	}
	binding := fact.Activation.Delegations[0]
	mutate(&binding)
	var err error
	if binding, err = sessions.SealDelegationCompactionBinding(binding); err != nil {
		t.Fatal(err)
	}
	activation := *fact.Activation
	activation.Delegations = []sessions.DelegationCompactionBinding{binding}
	if activation, err = sessions.SealContextCompactionActivation(activation); err != nil {
		t.Fatal(err)
	}
	fact.Activation = &activation
	if fact, err = sessions.SealContextCompactionLifecycleFact(fact); err != nil {
		t.Fatal(err)
	}
	bindingBody, factBody := mustJSON(t, binding), mustJSON(t, fact)
	var factTrigger, bindingTrigger string
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='context_compaction_plan_fact_immutable_update'`).Scan(&factTrigger); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='context_compaction_delegation_immutable_update'`).Scan(&bindingTrigger); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER context_compaction_plan_fact_immutable_update;
		DROP TRIGGER context_compaction_delegation_immutable_update;
		UPDATE context_compaction_plan_facts SET fact_digest=?,activation_digest=?,body=? WHERE fact_id=?;
		UPDATE context_compaction_delegations SET activation_digest=?,execution_context_digest=?,result_digest=?,binding_digest=?,body=? WHERE fact_id=?`,
		fact.Digest, activation.ActivationDigest, factBody, fact.ID,
		activation.ActivationDigest, binding.ExecutionContextDigest, binding.ResultDigest, binding.BindingDigest, bindingBody, fact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(factTrigger + ";" + bindingTrigger + ";"); err != nil {
		t.Fatal(err)
	}
}

func mutateDurableEvent(t *testing.T, db *sql.DB, id string, mutate func(*runtime.Event)) {
	t.Helper()
	var body []byte
	if err := db.QueryRow(`SELECT body FROM events WHERE id=?`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var event runtime.Event
	if json.Unmarshal(body, &event) != nil {
		t.Fatal("invalid event fixture")
	}
	mutate(&event)
	body, err := event.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE events SET body=? WHERE id=?; UPDATE event_log SET body_digest=? WHERE event_id=?`, body, id, streamBodyDigest(body), id); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
