package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func outcomeEventRequest(operation string, _ int64, code OutcomeSupervisionCode) OutcomeSupervisionEventRequest {
	return OutcomeSupervisionEventRequest{
		Version:            1,
		OperationID:        operation,
		CheckID:            strings.Repeat("9", 32),
		Code:               code,
		SkillScope:         "project",
		SkillName:          "generated_review",
		ActivationID:       strings.Repeat("a", 32),
		ActivationRevision: strings.Repeat("b", 64),
		PolicyID:           strings.Repeat("c", 64),
	}
}

func TestOutcomeSupervisionJournalConcurrentExactReconcile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	request := outcomeEventRequest(strings.Repeat("7", 64), 1, OutcomeSupervisionWaiting)
	stores := []*Store{first, second}
	results := make([]OutcomeSupervisionEvent, 8)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = stores[index%len(stores)].RecordOutcomeSupervisionEvent(ctx, request)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] || results[i].Sequence != 1 {
			t.Fatalf("concurrent retry %d diverged: %+v %v", i, results[i], errs[i])
		}
	}
	page, err := first.OutcomeSupervisionEvents(ctx, request.OperationID, 0, 10)
	if err != nil || page.HighWaterSequence != 1 || len(page.Items) != 1 || page.Items[0] != results[0] {
		t.Fatal(page, err)
	}
}

func TestOutcomeSupervisionJournalLifecycleRetryReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	operation := strings.Repeat("d", 64)
	waiting, err := store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 1, OutcomeSupervisionWaiting))
	if err != nil || waiting.Validate() != nil {
		t.Fatal(waiting, err)
	}
	retry, err := store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 1, OutcomeSupervisionWaiting))
	if err != nil || retry != waiting {
		t.Fatal("acknowledgement-loss retry changed event", retry, err)
	}
	ready, err := store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 2, OutcomeSupervisionReady))
	if err != nil || ready.Sequence != 2 {
		t.Fatal(ready, err)
	}
	rolled, err := store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 3, OutcomeSupervisionRolledBack))
	if err != nil || rolled.Sequence != 3 {
		t.Fatal(rolled, err)
	}
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 4, OutcomeSupervisionError)); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal operation accepted an append", err)
	}
	page, err := store.OutcomeSupervisionEvents(ctx, operation, 0, 2)
	if err != nil || page.Version != 1 || page.HighWaterSequence != 3 || !page.HasMore || len(page.Items) != 2 || page.Items[0] != waiting || page.Items[1] != ready {
		t.Fatal(page, err)
	}
	tail, err := store.OutcomeSupervisionEvents(ctx, operation, 2, 2)
	if err != nil || tail.HighWaterSequence != 3 || tail.HasMore || len(tail.Items) != 1 || tail.Items[0] != rolled {
		t.Fatal(tail, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := reopened.OutcomeSupervisionEvents(ctx, operation, 0, 10)
	if err != nil || replayed.HighWaterSequence != 3 || len(replayed.Items) != 3 || replayed.Items[2] != rolled {
		t.Fatal("restart replay changed journal", replayed, err)
	}
}

func TestOutcomeSupervisionJournalRejectsIdentityReuseAndUnsafeShape(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operation := strings.Repeat("e", 64)
	request := outcomeEventRequest(operation, 1, OutcomeSupervisionWaiting)
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.SkillName = "other_skill"
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed operation sequence was not fenced", err)
	}
	ready := outcomeEventRequest(operation, 2, OutcomeSupervisionReady)
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, ready); err != nil {
		t.Fatal(err)
	}
	backward := outcomeEventRequest(operation, 3, OutcomeSupervisionWaiting)
	backward.CheckID = strings.Repeat("8", 32)
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, backward); !errors.Is(err, ErrConflict) {
		t.Fatal("ready-to-waiting regression was not fenced", err)
	}
	unsafe := outcomeEventRequest(strings.Repeat("f", 64), 1, OutcomeSupervisionError)
	unsafe.SkillName = "prompt: reveal credentials"
	if unsafe.Validate() == nil {
		t.Fatal("free-form content entered structural event")
	}
	unsafe = outcomeEventRequest(strings.Repeat("f", 64), 1, OutcomeSupervisionError)
	unsafe.PolicyID = strings.Repeat("G", 64)
	if unsafe.Validate() == nil {
		t.Fatal("non-canonical policy identity accepted")
	}
}

func TestOutcomeSupervisionJournalDetectsBodyAndPrefixCorruption(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operation := strings.Repeat("1", 64)
	for sequence, code := range []OutcomeSupervisionCode{OutcomeSupervisionWaiting, OutcomeSupervisionReady, OutcomeSupervisionNoAction} {
		if _, err = store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, int64(sequence+1), code)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.db.ExecContext(ctx, `DELETE FROM outcome_supervision_events WHERE operation_id=? AND sequence=2`, operation); err != nil {
		t.Fatal(err)
	}
	if _, err = store.OutcomeSupervisionEvents(ctx, operation, 0, 10); !errors.Is(err, ErrOutcomeSupervisionCorrupt) {
		t.Fatal("gapped prefix accepted", err)
	}

	second := strings.Repeat("2", 64)
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(second, 1, OutcomeSupervisionWaiting)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE outcome_supervision_events SET code='ready' WHERE operation_id=?`, second); err != nil {
		t.Fatal(err)
	}
	if _, err = store.OutcomeSupervisionEvents(ctx, second, 0, 10); !errors.Is(err, ErrOutcomeSupervisionCorrupt) {
		t.Fatal("indexed/body mismatch accepted", err)
	}
}

func TestOutcomeSupervisionSchema47RejectsRetainedFutureAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	operation := strings.Repeat("3", 64)
	if _, err = store.RecordOutcomeSupervisionEvent(ctx, outcomeEventRequest(operation, 1, OutcomeSupervisionWaiting)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `PRAGMA user_version=46`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("retained schema-47 authority accepted as schema 46")
	}
}
