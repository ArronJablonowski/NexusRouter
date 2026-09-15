package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var allowOutcomeSupervision = OutcomeSupervisionGuard(func(context.Context, OutcomeSupervisionState, OutcomeSupervisionCheck) error { return nil })

func prepareOutcomeSupervisionFixture(t *testing.T, store *FileStore, scope string) (OutcomeSupervisionState, OutcomeSupervisionCheck) {
	t.Helper()
	state, check, err := store.PrepareOutcomeSupervision(context.Background(), scope, "outcomes", strings.Repeat("a", 64), time.Second, allowOutcomeSupervision)
	if err != nil || state.Validate() != nil || check.Validate() != nil {
		t.Fatal(state, check, err)
	}
	return state, check
}

func TestOutcomeSupervisionWaitingIsDurableAndDoesNotConsumeOutcome(t *testing.T) {
	store, path, key, predecessor, _, expected := regressionFixture(t)
	store.SetOutcomeRollback(true)
	state, check := prepareOutcomeSupervisionFixture(t, store, key.Scope)
	if check.Candidate.Current != expected || check.Candidate.Predecessor != predecessor ||
		state.PendingCheckID != check.CheckID || check.PreviousCursor != "" {
		t.Fatal(state, check)
	}
	assertNoOutcomeRecords := func(t *testing.T, store *FileStore) {
		t.Helper()
		var snapshot catalog
		if err := store.with(context.Background(), func(current *catalog) error { snapshot = *current; return nil }, false); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.OutcomeIntents) != 0 || len(snapshot.OutcomeSelections) != 0 || len(snapshot.OutcomeOperations) != 0 {
			t.Fatal("scheduling consumed outcome authority")
		}
	}
	assertNoOutcomeRecords(t, store)
	completed, err := store.CompleteOutcomeSupervisionCheck(context.Background(), check, OutcomeSupervisionCompletion{Code: "waiting"}, allowOutcomeSupervision)
	if err != nil || completed.After != key.Name || completed.PendingCheckID != "" || completed.Revision != state.Revision+1 {
		t.Fatal(completed, err)
	}
	assertNoOutcomeRecords(t, store)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetAutomatic(true)
	reopened.SetOutcomeRollback(true)
	got, err := reopened.OutcomeSupervisionState(context.Background(), key.Scope, "outcomes")
	if err != nil || got != completed {
		t.Fatal(got, err)
	}
	terminal, err := reopened.OutcomeSupervisionCheck(context.Background(), key.Scope, "outcomes", check.CheckID)
	if err != nil || terminal.Status != "completed" || terminal.Code != "waiting" {
		t.Fatal(terminal, err)
	}
	if retry, err := reopened.CompleteOutcomeSupervisionCheck(context.Background(), check, OutcomeSupervisionCompletion{Code: "waiting"}, allowOutcomeSupervision); err != nil || retry != completed {
		t.Fatal(retry, err)
	}
	assertNoOutcomeRecords(t, reopened)
}

func TestOutcomeSupervisionBoundWaitingRetainsStableOperation(t *testing.T) {
	store, _, key, _, _, _ := regressionFixture(t)
	store.SetOutcomeRollback(true)
	state, check := prepareOutcomeSupervisionFixture(t, store, key.Scope)
	bound, err := store.BindOutcomeSupervisionOperation(context.Background(), check, "stable-operation", allowOutcomeSupervision)
	if err != nil || bound.OutcomeOperationID != "stable-operation" {
		t.Fatal(bound, err)
	}
	completed, err := store.CompleteOutcomeSupervisionCheck(context.Background(), bound,
		OutcomeSupervisionCompletion{Code: "waiting"}, allowOutcomeSupervision)
	if err != nil || completed.Revision != state.Revision+1 {
		t.Fatal(completed, err)
	}
	terminal, err := store.OutcomeSupervisionCheck(context.Background(), key.Scope, "outcomes", check.CheckID)
	if err != nil || terminal.Status != "completed" || terminal.Code != "waiting" || terminal.OutcomeOperationID != "stable-operation" {
		t.Fatal(terminal, err)
	}
	if retry, err := store.CompleteOutcomeSupervisionCheck(context.Background(), bound,
		OutcomeSupervisionCompletion{Code: "waiting"}, allowOutcomeSupervision); err != nil || retry != completed {
		t.Fatal(retry, err)
	}
}

func TestOutcomeSupervisionConcurrentPrepareIsSingleOwnerAcrossStores(t *testing.T) {
	store, path, key, _, _, _ := regressionFixture(t)
	store.SetOutcomeRollback(true)
	other, err := Open(path, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetAutomatic(true)
	other.SetOutcomeRollback(true)
	type result struct {
		state OutcomeSupervisionState
		check OutcomeSupervisionCheck
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*FileStore{store, other} {
		wg.Add(1)
		go func(s *FileStore) {
			defer wg.Done()
			<-start
			state, check, err := s.PrepareOutcomeSupervision(context.Background(), key.Scope, "outcomes", strings.Repeat("a", 64), time.Second, allowOutcomeSupervision)
			results <- result{state, check, err}
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	values := make([]result, 0, 2)
	for value := range results {
		values = append(values, value)
	}
	if len(values) != 2 || values[0].err != nil || values[1].err != nil || values[0].state != values[1].state || values[0].check != values[1].check {
		t.Fatal(values)
	}
}

func TestOutcomeSupervisionBoundReceiptSettlesAfterRestart(t *testing.T) {
	store, path, expected, selection := outcomeRollbackFixture(t, true)
	state, check := prepareOutcomeSupervisionFixture(t, store, expected.Key.Scope)
	if check.Candidate.Current != expected {
		t.Fatal(check)
	}
	bound, err := store.BindOutcomeSupervisionOperation(context.Background(), check, "supervised", allowOutcomeSupervision)
	if err != nil || bound.OutcomeOperationID != "supervised" {
		t.Fatal(bound, err)
	}
	rebound, err := store.BindOutcomeSupervisionOperation(context.Background(), check, "supervised", allowOutcomeSupervision)
	if err != nil || rebound != bound {
		t.Fatal(rebound, err)
	}
	receipt, err := store.OutcomeRollbackPrepared(context.Background(), "supervised", "", expected, selection.Policy, selection,
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || receipt.Decision != "rolled_back" {
		t.Fatal(receipt, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetAutomatic(true)
	reopened.SetOutcomeRollback(true)
	pending, err := reopened.OutcomeSupervisionCheck(context.Background(), expected.Key.Scope, "outcomes", check.CheckID)
	if err != nil || pending != bound {
		t.Fatal(pending, err)
	}
	completed, err := reopened.CompleteOutcomeSupervisionCheck(context.Background(), pending,
		OutcomeSupervisionCompletion{Code: "evaluated", OutcomeOperationID: "supervised"}, allowOutcomeSupervision)
	if err != nil || completed.Revision != state.Revision+1 || completed.After != expected.Key.Name {
		t.Fatal(completed, err)
	}
}

func TestOutcomeSupervisionSkipsAlreadyAdjudicatedActivation(t *testing.T) {
	store, _, expected, selection := outcomeRollbackFixture(t, false)
	receipt, err := store.OutcomeRollbackPrepared(context.Background(), "already-checked", "", expected, selection.Policy, selection,
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || receipt.Decision != "no_action" {
		t.Fatal(receipt, err)
	}
	state, check, err := store.PrepareOutcomeSupervision(context.Background(), expected.Key.Scope, "outcomes", strings.Repeat("a", 64), time.Second, allowOutcomeSupervision)
	if err != nil || check != (OutcomeSupervisionCheck{}) || state.PendingCheckID != "" || state.After != "" || !state.NextDue.After(time.Now()) {
		t.Fatal(state, check, err)
	}
}

func TestOutcomeSupervisionStaleActivationIsCompletedNoAction(t *testing.T) {
	store, _, key, _, _, _ := regressionFixture(t)
	store.SetOutcomeRollback(true)
	_, check := prepareOutcomeSupervisionFixture(t, store, key.Scope)
	if err := store.RollbackAt(context.Background(), check.Candidate.Current, false); err != nil {
		t.Fatal(err)
	}
	state, err := store.CompleteOutcomeSupervisionCheck(context.Background(), check,
		OutcomeSupervisionCompletion{Code: "stale_activation"}, allowOutcomeSupervision)
	if err != nil || state.After != key.Name || state.PendingCheckID != "" {
		t.Fatal(state, err)
	}
	terminal, err := store.OutcomeSupervisionCheck(context.Background(), key.Scope, "outcomes", check.CheckID)
	if err != nil || terminal.Status != "completed" || terminal.Code != "stale_activation" {
		t.Fatal(terminal, err)
	}
}

func TestOutcomeSupervisionRejectsCorruptionAndPolicyDrift(t *testing.T) {
	store, path, key, _, _, _ := regressionFixture(t)
	store.SetOutcomeRollback(true)
	_, check := prepareOutcomeSupervisionFixture(t, store, key.Scope)
	if _, _, err := store.PrepareOutcomeSupervision(context.Background(), key.Scope, "outcomes", strings.Repeat("b", 64), time.Second, allowOutcomeSupervision); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var current catalog
	if err := store.read("catalog.json", &current); err != nil {
		t.Fatal(err)
	}
	current.OutcomeSupervisorChecks[check.CheckID] = func() OutcomeSupervisionCheck {
		bad := check
		bad.Candidate.Predecessor = bad.Candidate.Current.Active
		return bad
	}()
	if err := store.write("catalog.json", current, false); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	if _, err := store.OutcomeSupervisionState(context.Background(), key.Scope, "outcomes"); err == nil {
		t.Fatal("corrupt candidate accepted")
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestOutcomeSupervisionPrunesOldestTerminalCheckAtCapacity(t *testing.T) {
	store, _, key, _, _, _ := regressionFixture(t)
	store.SetOutcomeRollback(true)
	_, first := prepareOutcomeSupervisionFixture(t, store, key.Scope)
	state, err := store.CompleteOutcomeSupervisionCheck(context.Background(), first, OutcomeSupervisionCompletion{Code: "waiting"}, allowOutcomeSupervision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.with(context.Background(), func(c *catalog) error {
		original := c.OutcomeSupervisorChecks[first.CheckID]
		for i := 0; i < 999; i++ {
			copy := original
			copy.CheckID = fmt.Sprintf("retained-%03d", i)
			copy.Sequence = int64(i + 2)
			copy.FinishedAt = original.FinishedAt.Add(time.Duration(i+1) * time.Millisecond)
			c.OutcomeSupervisorChecks[copy.CheckID] = copy
		}
		state.Revision = 1001
		state.NextDue = time.Now().UTC().Add(-time.Second)
		c.OutcomeSupervisors[(Key{key.Scope, "outcomes"}).index()] = state
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	// The first due preparation wraps the completed lexical pass without a
	// check. A second due preparation reserves the candidate and prunes exactly
	// the deterministic oldest terminal tombstone.
	wrapped, empty, err := store.PrepareOutcomeSupervision(context.Background(), key.Scope, "outcomes", strings.Repeat("a", 64), time.Second, allowOutcomeSupervision)
	if err != nil || empty != (OutcomeSupervisionCheck{}) || wrapped.After != "" {
		t.Fatal(wrapped, empty, err)
	}
	if err = store.with(context.Background(), func(c *catalog) error {
		current := c.OutcomeSupervisors[(Key{key.Scope, "outcomes"}).index()]
		current.NextDue = time.Now().UTC().Add(-time.Second)
		c.OutcomeSupervisors[(Key{key.Scope, "outcomes"}).index()] = current
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	_, next, err := store.PrepareOutcomeSupervision(context.Background(), key.Scope, "outcomes", strings.Repeat("a", 64), time.Second, allowOutcomeSupervision)
	if err != nil || next.Validate() != nil {
		t.Fatal(next, err)
	}
	if _, err = store.OutcomeSupervisionCheck(context.Background(), key.Scope, "outcomes", first.CheckID); !errors.Is(err, ErrNotFound) {
		t.Fatal("oldest terminal record retained", err)
	}
	var count int
	if err = store.with(context.Background(), func(c *catalog) error { count = len(c.OutcomeSupervisorChecks); return nil }, false); err != nil || count != 1000 {
		t.Fatal(count, err)
	}
}
