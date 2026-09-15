package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func approvedContextCompactionPlanFixture(t *testing.T, store *Store) (runtime.ContextCompactionPlan, sessions.ContextCompactionOperationState) {
	t.Helper()
	ctx := context.Background()
	start, draft := compactionPlanStartFixture(t, store)
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
	review := sessions.SummaryReview{Version: 2, ID: "activation-review", AttemptID: start.AttemptID, Decision: "approved",
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
	state, err = store.ApproveContextCompactionPlan(ctx, approved)
	if err != nil {
		t.Fatal(err)
	}
	return plan, state
}

func planActivationEventFixture(t *testing.T, store *Store, plan runtime.ContextCompactionPlan, task string) runtime.Event {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1000, 0).UTC()
	start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: task + "-session", CorrelationID: task,
		Sequence: 1, Time: base, Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: plan.Compaction.SourceTaskID, Messages: append([]providers.Message(nil), plan.OriginalPrefix...)}}
	turn := runtime.Event{Version: 1, ID: task + "-turn", TaskID: task, SessionID: start.SessionID, CorrelationID: task,
		Sequence: 2, Time: base.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"}
	done := runtime.Event{Version: 1, ID: task + "-done", TaskID: task, SessionID: start.SessionID, CorrelationID: task,
		Sequence: 3, Time: base.Add(2 * time.Second), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt",
		Data: runtime.Data{Text: "live suffix", FinishReason: "stop"}}
	for _, event := range []runtime.Event{start, turn, done} {
		if err := store.Append(ctx, event.Sequence-1, event); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.TaskSnapshot(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := runtime.ExtendContextLineage(current.ContextLineage, task, 4, plan.Compaction, current.Messages)
	if err != nil {
		t.Fatal(err)
	}
	return runtime.Event{Version: 1, ID: task + "-compact", TaskID: task, SessionID: start.SessionID, CorrelationID: task,
		Sequence: 4, Time: base.Add(3 * time.Second), Kind: runtime.ContextCompacted,
		Data: runtime.Data{Compaction: plan.Compaction, ContextLineage: lineage, ParentTaskID: plan.Compaction.SourceTaskID,
			Messages: append([]providers.Message(nil), plan.ReplacementPrefix...), ReplacedMessages: len(plan.OriginalPrefix)}}
}

func TestAppendContextCompactionCommitsEventAndActivationAtomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activation.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	plan, approved := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "activation-task")
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatal(err)
	}
	state, err := store.ContextCompactionPlan(ctx, plan.OperationID)
	if err != nil || state.Status != sessions.ContextCompactionActivated || len(state.Facts) != len(approved.Facts)+1 {
		t.Fatalf("activation projection: state=%+v err=%v", state, err)
	}
	activation := state.Facts[len(state.Facts)-1].Activation
	if activation == nil || activation.EventID != event.ID || activation.EventSequence != event.Sequence || activation.LiveSuffixCount != 1 {
		t.Fatalf("activation evidence: %+v", activation)
	}
	wantSuffixDigest, err := sessions.ContextCompactionLiveSuffixDigest([]providers.Message{{Role: "assistant", Content: "live suffix"}})
	if err != nil || activation.LiveSuffixDigest != wantSuffixDigest {
		t.Fatalf("suffix binding: %q %v", activation.LiveSuffixDigest, err)
	}
	snapshot, err := store.TaskSnapshot(ctx, event.TaskID)
	wantMessages := append(append([]providers.Message(nil), plan.ReplacementPrefix...), providers.Message{Role: "assistant", Content: "live suffix"})
	if err != nil || snapshot.Sequence != event.Sequence || snapshot.Compaction == nil || !reflect.DeepEqual(snapshot.Messages, wantMessages) {
		t.Fatalf("activated snapshot: %+v err=%v", snapshot, err)
	}
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatalf("exact lost-ack replay failed: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatalf("reopened lost-ack replay failed: %v", err)
	}
	var eventCount, activationCount int
	if err = store.db.QueryRow(`SELECT
		(SELECT count(*) FROM events WHERE id=?),
		(SELECT count(*) FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated')`, event.ID, plan.OperationID).Scan(&eventCount, &activationCount); err != nil || eventCount != 1 || activationCount != 1 {
		t.Fatalf("duplicate atomic records: events=%d activations=%d err=%v", eventCount, activationCount, err)
	}
}

func TestContextCompactionActivationMissingFactFailsReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activation-corrupt.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "activation-task")
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_plan_fact_immutable_delete;
		DELETE FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated';
		CREATE TRIGGER context_compaction_plan_fact_immutable_delete BEFORE DELETE ON context_compaction_plan_facts
		BEGIN SELECT RAISE(ABORT,'context compaction plan fact immutable'); END;`, plan.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("planned ContextCompacted event without activation fact reopened")
	}
}

func TestAppendContextCompactionRollsBackBothRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, _ := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "rollback-task")
	if _, err = store.db.Exec(`CREATE TRIGGER reject_activation BEFORE INSERT ON context_compaction_plan_facts
		WHEN NEW.kind='activated' BEGIN SELECT RAISE(ABORT,'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err == nil {
		t.Fatal("injected activation failure was accepted")
	}
	var eventCount, activationCount, head int
	if err = store.db.QueryRow(`SELECT
		(SELECT count(*) FROM events WHERE id=?),
		(SELECT count(*) FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated'),
		(SELECT sequence FROM task_heads WHERE task_id=?)`, event.ID, plan.OperationID, event.TaskID).Scan(&eventCount, &activationCount, &head); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || activationCount != 0 || head != 3 {
		t.Fatalf("partial activation escaped rollback: events=%d activations=%d head=%d", eventCount, activationCount, head)
	}
	if _, err = store.db.Exec("DROP TRIGGER reject_activation"); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendContextCompaction(ctx, 3, event, plan); err != nil {
		t.Fatalf("retry after rollback failed: %v", err)
	}
}

func TestAppendContextCompactionConcurrentExactRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	plan, _ := approvedContextCompactionPlanFixture(t, first)
	event := planActivationEventFixture(t, first, plan, "concurrent-task")
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func(store *Store) { results <- store.AppendContextCompaction(ctx, 3, event, plan) }(store)
	}
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	var eventCount, activationCount int
	if err = first.db.QueryRow(`SELECT
		(SELECT count(*) FROM events WHERE id=?),
		(SELECT count(*) FROM context_compaction_plan_facts WHERE operation_id=? AND kind='activated')`, event.ID, plan.OperationID).Scan(&eventCount, &activationCount); err != nil || eventCount != 1 || activationCount != 1 {
		t.Fatalf("concurrent duplicate: events=%d activations=%d err=%v", eventCount, activationCount, err)
	}
}

func TestPlannedContextCompactionRequiresAtomicAppend(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "fail-closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, _ := approvedContextCompactionPlanFixture(t, store)
	event := planActivationEventFixture(t, store, plan, "fail-closed-task")
	if err = store.Append(ctx, 3, event); !errors.Is(err, sessions.ErrContextCompactionLifecycle) {
		t.Fatalf("plain append did not fail closed: %v", err)
	}
	if _, err = store.ContextCompactionPlanForAttempt(ctx, plan.Compaction.SummaryAttemptID); err != nil {
		t.Fatalf("summary lookup failed: %v", err)
	}
	var count int
	if err = store.db.QueryRow("SELECT count(*) FROM events WHERE id=?", event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("plain append persisted planned event: count=%d err=%v", count, err)
	}
	if _, err = store.ContextCompactionPlanForAttempt(ctx, "missing-attempt"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing summary lookup: %v", err)
	}
}
