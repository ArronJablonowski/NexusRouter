package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func outcomeRollbackFixture(t *testing.T, signal bool) (*FileStore, string, ActivationState, ComparisonSelectionReport) {
	t.Helper()
	s, path, key, a, b, state := regressionFixture(t)
	p := ComparisonSelectionPolicy{Version: 1, Privacy: "local_only", TasksPerVersion: 20, Comparison: comparisonFixturePolicy()}
	p.Comparison.Key = key
	p.Comparison.BaselineVersion = a
	p.Comparison.CandidateVersion = b
	digests := map[string]string{}
	if err := s.with(context.Background(), func(c *catalog) error {
		for _, m := range c.Skills[key.index()].Versions {
			digests[m.Version] = m.Digest
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	observations := comparisonFixtureOutcomes(20)
	for i := range observations {
		o := &observations[i]
		o.SkillContext.References[0].Scope = key.Scope
		o.SkillContext.References[0].Name = key.Name
		version := a
		if i >= 20 {
			version = b
		}
		o.SkillContext.References[0].Version = version
		o.SkillContext.References[0].Digest = digests[version]
		if !signal {
			o.Quality.Accepted = true
		}
	}
	comparison, err := CompareTaskOutcomes(observations, p.Comparison)
	if err != nil {
		t.Fatal(err)
	}
	r := ComparisonSelectionReport{Version: 1, Policy: p, Watermark: 40, Baseline: ComparisonWindow{Selected: 20, OldestOrdinal: 1, NewestOrdinal: 20}, Candidate: ComparisonWindow{Selected: 20, OldestOrdinal: 21, NewestOrdinal: 40}, Comparison: &comparison}
	if r.Validate() != nil {
		t.Fatal("fixture invalid")
	}
	s.SetOutcomeRollback(true)
	return s, path, state, r
}

func TestOutcomeRollbackConcurrentRevisionFence(t *testing.T) {
	s, _, state, selection := outcomeRollbackFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	selector := func(ctx context.Context) (ComparisonSelectionReport, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ComparisonSelectionReport{}, ctx.Err()
		}
		return selection, nil
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.OutcomeRollbackOnce(ctx, id, state, selection.Policy, selector, func(context.Context, OutcomeRollbackReceipt) error { return nil })
			results <- err
		}(id)
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			wg.Wait()
			t.Fatal("selector barrier timeout")
		}
	}
	close(release)
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	var c catalog
	if err := s.read("catalog.json", &c); err != nil || len(c.OutcomeOperations) != 1 || len(c.Skills[state.Key.index()].Activations) != 3 {
		t.Fatal(c, err)
	}
}

func TestOutcomeRollbackCatalogCorruptionRejected(t *testing.T) {
	for _, mode := range []string{"missing-receipt", "schema", "revision", "transition", "second-adjudication"} {
		t.Run(mode, func(t *testing.T) {
			s, path, state, selection := outcomeRollbackFixture(t, true)
			r, err := s.OutcomeRollbackOnce(context.Background(), "op", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			var c catalog
			if err = s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-receipt":
				delete(c.OutcomeOperations, "op")
			case "schema":
				c.Schema = 5
			case "revision":
				r.Expected.Revision = strings.Repeat("a", 64)
				c.OutcomeOperations["op"] = r
			case "transition":
				e := c.Skills[state.Key.index()]
				e.Activations[2].OutcomeOperationID = "other"
				c.Skills[state.Key.index()] = e
			case "second-adjudication":
				r.OperationID = "other"
				c.OutcomeOperations["other"] = r
			}
			if err = s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenReadOnly(path, []string{state.Key.Scope})
			if mode == "revision" {
				if err != nil {
					return
				}
				defer opened.Close()
				if _, err = opened.OutcomeRollbackOperation(context.Background(), state.Key, "op"); err == nil {
					t.Fatal("corrupt prefix lookup accepted")
				}
			} else if err == nil {
				opened.Close()
				t.Fatal("corruption accepted")
			}
		})
	}
}

func TestOutcomeRollbackReceiptAndHistoricalRetry(t *testing.T) {
	for _, signal := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_action", true: "rollback"}[signal], func(t *testing.T) {
			s, path, state, selection := outcomeRollbackFixture(t, signal)
			ctx := context.Background()
			calls, guards := 0, 0
			selector := func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }
			guard := func(_ context.Context, r OutcomeRollbackReceipt) error {
				guards++
				if r.Validate() != nil {
					t.Fatal("invalid guard receipt")
				}
				return nil
			}
			r, err := s.OutcomeRollbackOnce(ctx, "operation", state, selection.Policy, selector, guard)
			if err != nil || r.Validate() != nil || calls != 1 || guards != 1 || r.ActivationCount != 2 {
				t.Fatal(r, err, calls, guards)
			}
			if signal && r.Decision != "rolled_back" || !signal && r.Decision != "no_action" {
				t.Fatal(r)
			}
			if signal {
				if err = s.ActivateAt(ctx, r.After, state.Active, pass, false); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = s.RollbackAt(ctx, state, false); err != nil {
					t.Fatal(err)
				}
			}
			before := activationCatalogBytes(t, path)
			retry, err := s.OutcomeRollbackOnce(ctx, "operation", state, selection.Policy, selector, guard)
			if err != nil || !reflect.DeepEqual(retry, r) || calls != 1 || guards != 2 {
				t.Fatal(retry, err, calls, guards)
			}
			assertActivationCatalogUnchanged(t, path, before)
			reopened, err := Open(path, []string{state.Key.Scope})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.SetAutomatic(true)
			if _, err = reopened.OutcomeRollbackOnce(ctx, "operation", state, selection.Policy, selector, guard); !errors.Is(err, ErrDisabled) {
				t.Fatal("new store outcome flag not default off", err)
			}
			reopened.SetOutcomeRollback(true)
			persisted, err := reopened.OutcomeRollbackOnce(ctx, "operation", state, selection.Policy, selector, guard)
			if err != nil || !reflect.DeepEqual(persisted, r) || calls != 1 {
				t.Fatal("restart repeated selector", persisted, err, calls)
			}
			if _, err = s.OutcomeRollbackOnce(ctx, "alternate", state, selection.Policy, selector, guard); !errors.Is(err, ErrConflict) {
				t.Fatal("revision readjudicated", err)
			}
			ro, err := OpenReadOnly(path, []string{state.Key.Scope})
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			got, err := ro.OutcomeRollbackOperation(ctx, state.Key, "operation")
			if err != nil || !reflect.DeepEqual(got, r) {
				t.Fatal(got, err)
			}
			var c catalog
			if err = s.read("catalog.json", &c); err != nil || c.Schema != 6 {
				t.Fatal(c.Schema, err)
			}
			if signal {
				a := c.Skills[state.Key.index()].Activations[2]
				if a.OutcomeOperationID != "operation" || a.Regression != nil || a.Evidence != nil {
					t.Fatal("fabricated deterministic proof", a)
				}
			}
		})
	}
}

func TestOutcomeRollbackDenialsAndGuardOwnership(t *testing.T) {
	s, path, state, selection := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	calls := 0
	selector := func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }
	guard := func(context.Context, OutcomeRollbackReceipt) error { return nil }
	before := activationCatalogBytes(t, path)
	s.SetOutcomeRollback(false)
	if _, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, guard); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	s.SetOutcomeRollback(true)
	s.SetAutomatic(false)
	if _, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, guard); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	s.SetAutomatic(true)
	if calls != 0 {
		t.Fatal("disabled selector called")
	}
	for _, deny := range []OutcomeRollbackGuard{func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, func(context.Context, OutcomeRollbackReceipt) error { panic("private") }, func(context.Context, OutcomeRollbackReceipt) error { s.SetOutcomeRollback(false); return nil }} {
		if _, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, deny); err == nil {
			t.Fatal("guard bypassed")
		}
		assertActivationCatalogUnchanged(t, path, before)
		s.SetOutcomeRollback(true)
	}
	bad := selection
	bad.Comparison = new(ComparisonReport)
	*bad.Comparison = *selection.Comparison
	bad.Comparison.Candidate.Digest = strings.Repeat("f", 64)
	if _, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { return bad, nil }, guard); err == nil {
		t.Fatal("digest rebound")
	}
	assertActivationCatalogUnchanged(t, path, before)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.OutcomeRollbackOnce(canceled, "op", state, selection.Policy, selector, guard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, func(_ context.Context, r OutcomeRollbackReceipt) error {
		r.Selection.Comparison.Excluded["invented"] = 1
		return nil
	})
	if err != nil || r.Validate() != nil || len(r.Selection.Comparison.Excluded) != 0 {
		t.Fatal("guard alias", r, err)
	}
	if len(selection.Comparison.Excluded) != 0 {
		t.Fatal("selector alias")
	}
}

func TestOutcomeRollbackRejectsReactivatedCandidate(t *testing.T) {
	s, path, state, selection := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	if err := s.RollbackAt(ctx, state, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.ActivationState(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateAt(ctx, current, state.Active, pass, false); err != nil {
		t.Fatal(err)
	}
	current, err = s.ActivationState(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	calls := 0
	if _, err = s.OutcomeRollbackOnce(ctx, "new", current, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }); !errors.Is(err, ErrConflict) || calls != 0 {
		t.Fatal(err, calls)
	}
	assertActivationCatalogUnchanged(t, path, before)
}
