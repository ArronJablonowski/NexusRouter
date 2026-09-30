package telemetry

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func contextCompactionFixture(t *testing.T, store *Store, task string) (runtime.Event, sessions.SummaryReview, []providers.Message) {
	t.Helper()
	ctx := context.Background()
	attempt, review := reviewDraftFixture(t, store)
	if err := store.RecordSummaryReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	source, err := store.TaskSnapshot(ctx, attempt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	stableTail := []providers.Message{{Role: "user", Content: "current task prompt"}}
	initial := append(append([]providers.Message(nil), source.Messages...), stableTail...)
	start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 1, Time: time.Unix(500, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: source.TaskID, Messages: initial}}
	if err := store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	turnStart := runtime.Event{Version: 1, ID: task + "-turn-start", TaskID: task, SessionID: start.SessionID, CorrelationID: task, Sequence: 2, Time: start.Time.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt"}
	if err := store.Append(ctx, 1, turnStart); err != nil {
		t.Fatal(err)
	}
	turnDone := runtime.Event{Version: 1, ID: task + "-turn-done", TaskID: task, SessionID: start.SessionID, CorrelationID: task, Sequence: 3, Time: start.Time.Add(2 * time.Second), Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "live answer", FinishReason: "stop"}}
	if err := store.Append(ctx, 2, turnDone); err != nil {
		t.Fatal(err)
	}
	replacement, checkpoint, err := sessions.PrepareContinuation(source, attempt.Draft.Request)
	if err != nil {
		t.Fatal(err)
	}
	replacement = append(replacement, stableTail...)
	checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = attempt.ID, review.ID
	current, err := store.TaskSnapshot(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := runtime.ExtendContextLineage(current.ContextLineage, task, 4, checkpoint, current.Messages)
	if err != nil {
		t.Fatal(err)
	}
	activation := runtime.Event{Version: 1, ID: task + "-compact", TaskID: task, SessionID: start.SessionID, CorrelationID: task, Sequence: 4, Time: start.Time.Add(3 * time.Second), Kind: runtime.ContextCompacted, Data: runtime.Data{Compaction: checkpoint, ContextLineage: lineage, ParentTaskID: source.TaskID, Messages: replacement, ReplacedMessages: len(initial)}}
	if err := activation.Validate(); err != nil {
		t.Fatalf("activation fixture: %v epoch=%#v checkpoint=%#v", err, lineage.Epochs[len(lineage.Epochs)-1], *checkpoint)
	}
	return activation, review, initial
}

func refreshCompactionLineage(t *testing.T, store *Store, event *runtime.Event, messages []providers.Message) {
	t.Helper()
	current, err := store.TaskSnapshot(context.Background(), event.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if messages == nil {
		messages = current.Messages
	}
	event.Data.ContextLineage, err = runtime.ExtendContextLineage(current.ContextLineage, event.TaskID, event.Sequence, event.Data.Compaction, messages)
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextCompactionSupportsCompletePostActivationLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "lifecycle")
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal(err)
	}
	turnStart := runtime.Event{Version: 1, ID: "lifecycle-turn-2", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 5, Time: activation.Time.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{ModelID: "model", ProviderID: "provider"}}
	turnDone := runtime.Event{Version: 1, ID: "lifecycle-done-2", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 6, Time: activation.Time.Add(2 * time.Second), Kind: runtime.TurnCompleted, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{Text: "final answer", FinishReason: "stop", Usage: &providers.Usage{InputTokens: 12, OutputTokens: 3}}}
	accepted := true
	evaluated := runtime.Event{Version: 1, ID: "lifecycle-evaluation", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 7, Time: activation.Time.Add(3 * time.Second), Kind: runtime.EvaluationRecorded, TurnID: "turn-2", AttemptID: "attempt-2", Data: runtime.Data{Accepted: &accepted, Code: "deterministic.nonempty_text.v1", ModelID: "model", ProviderID: "provider", Domain: "chat", Profile: "default"}}
	completed := runtime.Event{Version: 1, ID: "lifecycle-completed", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 8, Time: activation.Time.Add(4 * time.Second), Kind: runtime.TaskCompleted, TurnID: "turn-2", AttemptID: "attempt-2"}
	for _, item := range []runtime.Event{turnStart, turnDone, evaluated, completed} {
		if err := store.Append(ctx, item.Sequence-1, item); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.Compaction == nil || snapshot.Messages[len(snapshot.Messages)-1].Content != "final answer" {
		t.Fatal("terminal snapshot did not replay compacted lifecycle", snapshot.State, err)
	}
	events, err := store.Read(ctx, activation.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Terminal submission admission metadata is orthogonal to this store
	// fixture; add the same valid projection fields used by a real app task.
	events[0].Data.SubmissionID = "lifecycle-submission"
	events[0].Data.ModelID, events[0].Data.ProviderID = "model", "provider"
	events[0].Data.Domain, events[0].Data.Profile = "chat", "default"
	for i := range events {
		if events[i].Kind == runtime.TurnStarted {
			events[i].Data.ModelID, events[i].Data.ProviderID = "model", "provider"
		}
	}
	outcome, err := sessions.ProjectTerminalSubmission(events)
	if err != nil || outcome.State != "succeeded" || outcome.Result == nil || outcome.Result.Text != "final answer" {
		t.Fatal("terminal projection rejected compacted lifecycle", outcome, err)
	}
}

func TestPostCompactionJournalBudgetPreservesTerminalRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "recovery-budget")
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal(err)
	}
	turn := runtime.Event{Version: 1, ID: "recovery-turn", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 5, Time: activation.Time.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn-2", AttemptID: "attempt-2"}
	if err := store.Append(ctx, 4, turn); err != nil {
		t.Fatal(err)
	}
	var used int
	if err := store.db.QueryRow(`SELECT SUM(length(CAST(body AS BLOB))) FROM events WHERE task_id=?`, activation.TaskID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	delta := turn
	delta.ID, delta.Sequence, delta.Kind, delta.Time = "oversized-delta", 6, runtime.ModelDelta, turn.Time.Add(time.Second)
	delta.Data.Text = "x"
	base, err := delta.Encode()
	if err != nil {
		t.Fatal(err)
	}
	available := sessions.MaxEventPageBytes - postCompactionTerminalReserveBytes - used
	padding := available - (len(base) - 1) + 1
	if padding < 1 || padding > sessions.MaxEventPageBytes {
		t.Fatal("invalid post-activation boundary fixture", padding)
	}
	delta.Data.Text = strings.Repeat("x", padding)
	if err := store.Append(ctx, 5, delta); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("post-activation work consumed terminal reserve", err)
	}
	failed := runtime.Event{Version: 1, ID: "recovery-failed", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 6, Time: turn.Time.Add(2 * time.Second), Kind: runtime.TaskFailed, TurnID: turn.TurnID, AttemptID: turn.AttemptID, Data: runtime.Data{Code: "persistence_limit"}}
	if err := store.Append(ctx, 5, failed); err != nil {
		t.Fatal("terminal recovery could not use reserve", err)
	}
	snapshot, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || snapshot.State != "failed" || snapshot.Sequence != 6 || snapshot.Compaction == nil {
		t.Fatal("recovered task is not replayable", snapshot.State, snapshot.Sequence, err)
	}
}

func TestPostCompactionEventCountPreservesTerminalRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "event-budget")
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal(err)
	}
	turn := runtime.Event{Version: 1, ID: "event-budget-turn", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 5, Time: activation.Time.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn-2", AttemptID: "attempt-2"}
	if err := store.Append(ctx, 4, turn); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := int64(6); sequence < sessions.MaxTaskEvents; sequence++ {
		delta := runtime.Event{Version: 1, ID: fmt.Sprintf("event-budget-delta-%d", sequence), TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: sequence, Time: turn.Time.Add(time.Duration(sequence) * time.Microsecond), Kind: runtime.ModelDelta, TurnID: turn.TurnID, AttemptID: turn.AttemptID, Data: runtime.Data{Text: "x"}}
		body, encodeErr := delta.Encode()
		if encodeErr != nil {
			tx.Rollback()
			t.Fatal(encodeErr)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events VALUES (?,?,?,?)`, delta.ID, delta.TaskID, delta.Sequence, body); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = appendEventLog(ctx, tx, delta, body); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_heads SET sequence=? WHERE task_id=?`, sessions.MaxTaskEvents-1, activation.TaskID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	extra := runtime.Event{Version: 1, ID: "event-budget-extra", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: sessions.MaxTaskEvents, Time: turn.Time.Add(time.Second), Kind: runtime.ModelDelta, TurnID: turn.TurnID, AttemptID: turn.AttemptID, Data: runtime.Data{Text: "x"}}
	if err := store.Append(ctx, sessions.MaxTaskEvents-1, extra); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("post-activation work consumed terminal event slot", err)
	}
	failed := extra
	failed.ID, failed.Kind, failed.Data = "event-budget-failed", runtime.TaskFailed, runtime.Data{Code: "event_limit"}
	if err := store.Append(ctx, sessions.MaxTaskEvents-1, failed); err != nil {
		t.Fatal("terminal could not use reserved event slot", err)
	}
	snapshot, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || snapshot.State != "failed" || snapshot.Sequence != sessions.MaxTaskEvents || snapshot.Compaction == nil {
		t.Fatal("event-count recovery is not replayable", snapshot.State, snapshot.Sequence, err)
	}
}

func TestContextCompactionGateCommitsOnceAndReplays(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, approved, initial := contextCompactionFixture(t, store, "continuation")
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || snapshot.Compaction == nil || snapshot.Compaction.SummaryReviewID == "" || len(snapshot.Messages) != len(activation.Data.Messages)+1 || snapshot.Messages[len(snapshot.Messages)-1].Content != "live answer" || len(initial) == len(snapshot.Messages) {
		t.Fatal("activation did not replay", snapshot, err)
	}
	second := activation
	second.ID, second.Sequence = "second-compaction", 5
	refreshCompactionLineage(t, store, &second, nil)
	if err := store.Append(ctx, 4, second); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("second activation admitted", err)
	}
	rejected := approved
	rejected.ID, rejected.PreviousID, rejected.Decision, rejected.Note = "review-after-activation", approved.ID, "rejected", "revoked for future use"
	rejected.Time = approved.Time.Add(time.Second)
	if err := store.RecordSummaryReview(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal("exact acknowledgement retry failed after revocation", err)
	}
	var events, ledger int
	if err := store.db.QueryRow(`SELECT count(*) FROM events WHERE task_id=? AND json_extract(body,'$.kind')='context.compacted'`, activation.TaskID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM event_log l JOIN events e ON e.id=l.event_id WHERE e.task_id=? AND json_extract(e.body,'$.kind')='context.compacted'`, activation.TaskID).Scan(&ledger); err != nil || events != 1 || ledger != 1 {
		t.Fatal("activation was not exactly once", events, ledger, err)
	}
}

func TestContextCompactionGateRejectsDriftAndRollsBack(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*runtime.Event){
		"foreign parent": func(e *runtime.Event) { e.Data.ParentTaskID = "foreign" },
		"prefix count":   func(e *runtime.Event) { e.Data.ReplacedMessages-- },
		"replacement":    func(e *runtime.Event) { e.Data.Messages[0].Content = "forged" },
		"review":         func(e *runtime.Event) { e.Data.Compaction.SummaryReviewID = "foreign" },
		"source digest":  func(e *runtime.Event) { e.Data.Compaction.SourceDigest = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			activation, _, _ := contextCompactionFixture(t, store, "continuation")
			mutate(&activation)
			if err := store.Append(ctx, 3, activation); err == nil {
				t.Fatal("drift admitted")
			}
			var sequence int64
			if err := store.db.QueryRow(`SELECT sequence FROM task_heads WHERE task_id=?`, activation.TaskID).Scan(&sequence); err != nil || sequence != 3 {
				t.Fatal("failed activation changed head", sequence, err)
			}
		})
	}
}

func TestContextCompactionRequiresCompletedTurn(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, initial := contextCompactionFixture(t, store, "ordinary")
	start := runtime.Event{Version: 1, ID: "early-start", TaskID: "early", SessionID: "early-session", CorrelationID: "early", Sequence: 1, Time: time.Unix(600, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: activation.Data.ParentTaskID, Messages: initial}}
	if err := store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	activation.ID, activation.TaskID, activation.SessionID, activation.CorrelationID, activation.Sequence = "early-compact", "early", "early-session", "early", 2
	refreshCompactionLineage(t, store, &activation, nil)
	if err := store.Append(ctx, 1, activation); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("pre-turn activation admitted", err)
	}
}

func TestContextCompactionRejectsChangedFrozenInitialPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "continuation")
	if _, err := store.db.Exec(`UPDATE events SET body=json_set(body,'$.data.messages[0].content','changed after planning') WHERE id='continuation-start'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 3, activation); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("changed source prefix admitted", err)
	}
	var sequence int64
	if err := store.db.QueryRow(`SELECT sequence FROM task_heads WHERE task_id=?`, activation.TaskID).Scan(&sequence); err != nil || sequence != 3 {
		t.Fatal("corrupt-prefix denial changed head", sequence, err)
	}
}

func TestContextCompactionRejectsCumulativeHistoryOverflow(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activation, _, _ := contextCompactionFixture(t, store, "large-history")
	turn := runtime.Event{Version: 1, ID: "large-turn", TaskID: activation.TaskID, SessionID: activation.SessionID, CorrelationID: activation.TaskID, Sequence: 4, Time: activation.Time, Kind: runtime.TurnStarted, TurnID: "large", AttemptID: "large-attempt"}
	delta := turn
	delta.ID, delta.Sequence, delta.Kind, delta.Time = "large-delta", 5, runtime.ModelDelta, turn.Time.Add(time.Second)
	delta.Data.Text = "x"
	done := turn
	done.ID, done.Sequence, done.Kind, done.Time = "large-done", 6, runtime.TurnCompleted, turn.Time.Add(2*time.Second)
	done.Data.FinishReason = "stop"
	activation.Sequence, activation.Time = 7, done.Time.Add(time.Second)
	lineageMessages, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	prospectiveMessages := append(append([]providers.Message(nil), lineageMessages.Messages...), providers.Message{Role: "assistant"})
	refreshCompactionLineage(t, store, &activation, prospectiveMessages)

	prior, err := store.Read(ctx, activation.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, item := range prior {
		body, encodeErr := item.Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		total += len(body)
	}
	for _, item := range []runtime.Event{turn, done} {
		body, encodeErr := item.Encode()
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		total += len(body)
	}
	deltaOne, err := delta.Encode()
	if err != nil {
		t.Fatal(err)
	}
	activationBody, err := activation.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Leave the pre-activation history valid but less than one activation body
	// away from the activation budget. The remaining six MiB are deliberately
	// reserved for post-activation output and lifecycle facts.
	target := sessions.MaxContextCompactionActivationBytes - len(activationBody)/2
	padding := target - total - (len(deltaOne) - 1)
	if padding < 1 || padding > sessions.MaxEventPageBytes {
		t.Fatal("invalid boundary fixture", padding)
	}
	delta.Data.Text = strings.Repeat("x", padding)
	if err := store.Append(ctx, 3, turn); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 4, delta); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, 5, done); err != nil {
		t.Fatal(err)
	}
	before, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || before.Sequence != 6 || before.State != "running" {
		t.Fatal("near-limit source was not replayable", before.Sequence, err)
	}
	if err := store.Append(ctx, 6, activation); !errors.Is(err, runtime.ErrJournalLimit) || !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("cumulative overflow admitted", err)
	}
	after, err := store.TaskSnapshot(ctx, activation.TaskID)
	if err != nil || after.Sequence != 6 || after.Compaction != nil {
		t.Fatal("rejected activation damaged prior history", after.Sequence, err)
	}
}

func TestContextCompactionReviewRaceAndLedgerRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "compaction.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	activation, approved, _ := contextCompactionFixture(t, store, "continuation")
	rejected := approved
	rejected.ID, rejected.PreviousID, rejected.Decision, rejected.Note = "rejected", approved.ID, "rejected", "revoked before later use"
	rejected.Time = approved.Time.Add(time.Second)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); <-start; results <- store.Append(ctx, 3, activation) }()
	go func() { defer wait.Done(); <-start; results <- other.RecordSummaryReview(ctx, rejected) }()
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes < 1 || successes > 2 {
		t.Fatal("transactions did not serialize", successes)
	}

	rollbackStore, err := Open(ctx, filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackStore.Close()
	rollback, _, _ := contextCompactionFixture(t, rollbackStore, "rollback-task")
	if _, err := rollbackStore.db.Exec(`CREATE TRIGGER fail_compaction_ledger BEFORE INSERT ON event_log WHEN NEW.event_id='rollback-task-compact' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := rollbackStore.Append(ctx, 3, rollback); err == nil {
		t.Fatal("injected ledger failure ignored")
	}
	var sequence int64
	if err := rollbackStore.db.QueryRow(`SELECT sequence FROM task_heads WHERE task_id=?`, rollback.TaskID).Scan(&sequence); err != nil || sequence != 3 {
		t.Fatal("ledger failure partially committed", sequence, err)
	}
}
