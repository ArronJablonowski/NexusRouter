package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/classification"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func intentClassificationFixture() classification.Attempt {
	return classification.Attempt{
		Version: 1, ID: "classifier-attempt", TaskID: "future-task", SessionID: "session",
		SubmissionID: "submission", RequestDigest: strings.Repeat("a", 64), ConfigID: strings.Repeat("b", 64),
		Model: "classifier-model", Provider: "provider", EstimatedCost: .01,
		Status: classification.AttemptStarted, StartedAt: time.Unix(100, 0).UTC(),
	}
}

func intentClassificationUse(t *testing.T, attempt classification.Attempt) *runtime.IntentClassificationUse {
	t.Helper()
	use := &runtime.IntentClassificationUse{Version: 1, AttemptID: attempt.ID, Status: attempt.Status, Code: attempt.Code}
	if attempt.Decision != nil {
		body, err := json.Marshal(attempt.Decision)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		use.DecisionDigest = hex.EncodeToString(digest[:])
	}
	return use
}

func completedIntentClassification(started classification.Attempt) classification.Attempt {
	completed := started
	completed.Status = classification.AttemptCompleted
	completed.FinishedAt = started.StartedAt.Add(time.Second)
	completed.Elapsed = 500 * time.Millisecond
	completed.Usage = &providers.Usage{InputTokens: 12, OutputTokens: 3}
	completed.Decision = &classification.Decision{Version: classification.DecisionVersion, Domain: "code", Capabilities: []string{"debug", "review"}}
	return completed
}

func failedIntentClassification(started classification.Attempt) classification.Attempt {
	failed := started
	failed.Status = classification.AttemptFailed
	failed.Code = classification.CodeProviderFailed
	failed.FinishedAt = started.StartedAt.Add(time.Second)
	failed.Elapsed = 500 * time.Millisecond
	return failed
}

func TestIntentClassificationAttemptLifecycleIdempotencyAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	started := intentClassificationFixture()
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal("exact begin retry", err)
	}
	changed := started
	changed.Model = "other-model"
	if err = store.BeginIntentClassification(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed begin retry", err)
	}
	other := started
	other.ID, other.TaskID = "other-attempt", "other-task"
	if err = store.BeginIntentClassification(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate task/submission binding", err)
	}
	// Empty submission identities do not collapse distinct pre-admission work,
	// while each attempt still owns a globally unique future task identity.
	unboundA := started
	unboundA.ID, unboundA.TaskID, unboundA.SessionID, unboundA.SubmissionID = "unbound-a", "unbound-task-a", "unbound-task-a", ""
	unboundB := unboundA
	unboundB.ID, unboundB.TaskID, unboundB.SessionID = "unbound-b", "unbound-task-b", "unbound-task-b"
	if err = store.BeginIntentClassification(ctx, unboundA); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginIntentClassification(ctx, unboundB); err != nil {
		t.Fatal(err)
	}
	unboundCollision := unboundB
	unboundCollision.ID = "unbound-collision"
	if err = store.BeginIntentClassification(ctx, unboundCollision); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate unbound task identity", err)
	}
	completed := completedIntentClassification(started)
	if err = store.FinishIntentClassification(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishIntentClassification(ctx, completed); err != nil {
		t.Fatal("exact terminal retry", err)
	}
	if err = store.FinishIntentClassification(ctx, failedIntentClassification(started)); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal rewrite", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.IntentClassificationAttempt(ctx, started.ID)
	if err != nil || got.Status != classification.AttemptCompleted || got.Decision == nil || got.Decision.Domain != "code" {
		t.Fatal(got, err)
	}
	got, err = store.IntentClassificationAttemptForSubmission(ctx, started.SubmissionID)
	if err != nil || got.ID != started.ID || got.Status != classification.AttemptCompleted {
		t.Fatal("submission lookup", got, err)
	}
	if _, err = store.IntentClassificationAttemptForSubmission(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing submission lookup", err)
	}
	if _, err = store.IntentClassificationAttemptForSubmission(ctx, ""); !errors.Is(err, classification.ErrInvalidInput) {
		t.Fatal("empty submission lookup", err)
	}
	if err = store.BeginIntentClassification(ctx, started); err == nil {
		t.Fatal("readonly begin succeeded")
	}
}

func TestIntentClassificationAttemptConcurrentTerminalCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
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
	started := intentClassificationFixture()
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal(err)
	}
	completed, failed := completedIntentClassification(started), failedIntentClassification(started)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, operation := range []func() error{
		func() error { return store.FinishIntentClassification(ctx, completed) },
		func() error { return other.FinishIntentClassification(ctx, failed) },
	} {
		wait.Add(1)
		go func(operation func() error) {
			defer wait.Done()
			results <- operation()
		}(operation)
	}
	wait.Wait()
	close(results)
	wins, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			wins++
		case errors.Is(result, ErrConflict):
			conflicts++
		default:
			t.Fatal(result)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	got, err := store.IntentClassificationAttempt(ctx, started.ID)
	if err != nil || got.Status != classification.AttemptCompleted && got.Status != classification.AttemptFailed {
		t.Fatal(got, err)
	}
}

func TestIntentClassificationAttemptFailureIsAtomicAndCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := intentClassificationFixture()
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER reject_classifier_terminal
		BEFORE UPDATE ON intent_classification_attempts WHEN NEW.status<>OLD.status
		BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishIntentClassification(ctx, completedIntentClassification(started)); err == nil {
		t.Fatal("injected terminal failure ignored")
	}
	got, err := store.IntentClassificationAttempt(ctx, started.ID)
	if err != nil || got.Status != classification.AttemptStarted {
		t.Fatal("partial terminal persisted", got, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER reject_classifier_terminal;
		UPDATE intent_classification_attempts SET body=json_set(body,'$.task_id','forged') WHERE id=?`, started.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IntentClassificationAttempt(ctx, started.ID); !errors.Is(err, classification.ErrInvalidInput) {
		t.Fatal("corrupt body identity accepted", err)
	}
	if _, err = store.db.Exec(`UPDATE intent_classification_attempts SET body=json_set(body,'$.task_id',task_id),status='failed' WHERE id=?`, started.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IntentClassificationAttempt(ctx, started.ID); !errors.Is(err, classification.ErrInvalidInput) {
		t.Fatal("corrupt status binding accepted", err)
	}
	if err = store.FinishIntentClassification(ctx, completedIntentClassification(classification.Attempt{ID: "missing"})); !errors.Is(err, classification.ErrInvalidInput) {
		t.Fatal("invalid terminal accepted", err)
	}
	if _, err = store.IntentClassificationAttempt(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing read", err)
	}
}

func TestTaskStartedBindsIntentClassificationAndAccountsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := intentClassificationFixture()
	started.TaskID, started.SubmissionID = "task", ""
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal(err)
	}
	completed := completedIntentClassification(started)
	if err = store.FinishIntentClassification(ctx, completed); err != nil {
		t.Fatal(err)
	}
	startEvent := event("classified-start", 1, runtime.TaskStarted)
	startEvent.Data.ConfigID = completed.ConfigID
	startEvent.Data.IntentClassification = intentClassificationUse(t, completed)
	if err = store.Append(ctx, 0, startEvent); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 0, startEvent); err != nil {
		t.Fatal("exact TaskStarted retry", err)
	}
	record, err := store.CurrentUsage(ctx, usageID(accounting.Classifier, completed.ID))
	if err != nil || record.Role != accounting.Classifier || record.EvidenceID != startEvent.ID ||
		record.OperationID != completed.ID || record.Disposition != accounting.Completed ||
		record.Usage == nil || *record.Usage != *completed.Usage {
		t.Fatal(record, err)
	}
	var records int
	if err = store.db.QueryRow(`SELECT count(*) FROM usage_records WHERE role=?`, accounting.Classifier).Scan(&records); err != nil || records != 1 {
		t.Fatalf("classifier records=%d err=%v", records, err)
	}
	route := event("classified-route", 2, runtime.RouteSelected)
	route.RouteID = "route"
	route.Data.ModelID, route.Data.ProviderID = "model", "provider"
	if err = store.Append(ctx, 1, route); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read(ctx, "task", 0, 10)
	if err != nil || len(events) != 2 || events[0].Kind != runtime.TaskStarted || events[1].Kind != runtime.RouteSelected {
		t.Fatal("event ordering", events, err)
	}
}

func TestTaskStartedRejectsClassifierMismatchAndFailedUsage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := intentClassificationFixture()
	started.ID, started.TaskID, started.SubmissionID = "mismatch-attempt", "mismatch-task", ""
	if err = store.BeginIntentClassification(ctx, started); err != nil {
		t.Fatal(err)
	}
	completed := completedIntentClassification(started)
	if err = store.FinishIntentClassification(ctx, completed); err != nil {
		t.Fatal(err)
	}
	bad := event("mismatch-start", 1, runtime.TaskStarted)
	bad.TaskID, bad.Data.ConfigID = completed.TaskID, completed.ConfigID
	bad.Data.IntentClassification = intentClassificationUse(t, completed)
	bad.Data.IntentClassification.DecisionDigest = strings.Repeat("f", 64)
	if err = store.Append(ctx, 0, bad); !errors.Is(err, classification.ErrInvalidInput) {
		t.Fatal("mismatched decision digest", err)
	}
	var heads int
	if err = store.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, bad.TaskID).Scan(&heads); err != nil || heads != 0 {
		t.Fatal("rejected start left task state", heads, err)
	}

	failedStart := intentClassificationFixture()
	failedStart.ID, failedStart.TaskID, failedStart.SubmissionID = "failed-attempt", "failed-task", ""
	if err = store.BeginIntentClassification(ctx, failedStart); err != nil {
		t.Fatal(err)
	}
	failed := failedIntentClassification(failedStart)
	failed.Usage = &providers.Usage{InputTokens: 9, OutputTokens: 1}
	if err = store.FinishIntentClassification(ctx, failed); err != nil {
		t.Fatal(err)
	}
	failedEvent := event("failed-classifier-start", 1, runtime.TaskStarted)
	failedEvent.TaskID, failedEvent.Data.ConfigID = failed.TaskID, failed.ConfigID
	failedEvent.Data.IntentClassification = intentClassificationUse(t, failed)
	if err = store.Append(ctx, 0, failedEvent); err != nil {
		t.Fatal(err)
	}
	record, err := store.CurrentUsage(ctx, usageID(accounting.Classifier, failed.ID))
	if err != nil || record.Disposition != accounting.Failed || !accounting.SameUsage(record.Usage, failed.Usage) {
		t.Fatal("failed attribution lost known usage", record, err)
	}
}
