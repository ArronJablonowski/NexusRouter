package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOutcomeIntentPersistsBeforeSingleSelectorAndConsumesFailure(t *testing.T) {
	for _, mode := range []string{"selector-error", "selector-panic", "cancel", "guard-deny", "guard-panic", "guard-kill", "digest", "model"} {
		t.Run(mode, func(t *testing.T) {
			s, path, state, selection := outcomeRollbackFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			selector := func(context.Context) (ComparisonSelectionReport, error) {
				calls++
				i, err := s.OutcomeRollbackIntent(ctx, state.Key, "op")
				if err != nil || i.Validate() != nil || i.Expected != state || i.Policy != selection.Policy {
					t.Fatal("intent not visible before callback", i, err)
				}
				switch mode {
				case "selector-error":
					return ComparisonSelectionReport{}, ErrInvalid
				case "selector-panic":
					panic("private")
				case "cancel":
					cancel()
				case "digest":
					selection.Comparison.Candidate.Digest = strings.Repeat("f", 64)
				case "model":
					selection.ConfiguredModelID = "other"
					selection.Comparison.ConfiguredModelID = "other"
				}
				return selection, nil
			}
			guard := func(context.Context, OutcomeRollbackReceipt) error {
				switch mode {
				case "guard-deny":
					return ErrInvalid
				case "guard-panic":
					panic("private")
				case "guard-kill":
					s.SetOutcomeRollback(false)
				}
				return nil
			}
			if r, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, guard); err == nil || r.Version != 0 || calls != 1 {
				t.Fatal(r, err, calls)
			}
			s.SetOutcomeRollback(true)
			ctx = context.Background()
			current, err := s.ActivationState(ctx, state.Key)
			if err != nil || current != state {
				t.Fatal("failed selection changed activation", current, err)
			}
			if _, err = s.OutcomeRollbackOperation(ctx, state.Key, "op"); !errors.Is(err, ErrNotFound) {
				t.Fatal("fabricated receipt", err)
			}
			before := activationCatalogBytes(t, path)
			if _, err = s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, selector, guard); !errors.Is(err, ErrOutcomePending) {
				t.Fatal(err)
			}
			if _, err = s.OutcomeRollbackOnce(ctx, "alternate", state, selection.Policy, selector, guard); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			changed := selection.Policy
			changed.Comparison.MinDrop = .2
			if _, err = s.OutcomeRollbackOnce(ctx, "op", state, changed, selector, guard); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("selected again", calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestOutcomeIntentGuardDenialBeforePersistence(t *testing.T) {
	for _, mode := range []string{"deny", "panic", "kill", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, path, state, selection := outcomeRollbackFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			before := activationCatalogBytes(t, path)
			guard := func(_ context.Context, i OutcomeRollbackIntent) error {
				if i.Validate() != nil || i.ConfiguredModelID != "brain" {
					t.Fatal(i)
				}
				switch mode {
				case "deny":
					return ErrInvalid
				case "panic":
					panic("private")
				case "kill":
					s.SetOutcomeRollback(false)
				case "cancel":
					cancel()
				}
				return nil
			}
			_, err := s.OutcomeRollbackOnceGuarded(ctx, "op", "brain", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }, guard)
			if err == nil || calls != 0 {
				t.Fatal(err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestOutcomeIntentLegacySchema6ReceiptCompatibility(t *testing.T) {
	s, path, state, selection := outcomeRollbackFixture(t, false)
	ctx := context.Background()
	r, err := s.OutcomeRollbackOnce(ctx, "old", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var c catalog
	if err = s.read("catalog.json", &c); err != nil {
		t.Fatal(err)
	}
	r.IntentID = ""
	r.Selection.ConfiguredModelID = "legacy-brain"
	r.Selection.Comparison.ConfiguredModelID = "legacy-brain"
	c.OutcomeOperations["old"] = r
	c.OutcomeIntents = nil
	c.Schema = 6
	if err = s.write("catalog.json", c, false); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	got, err := s.OutcomeRollbackOnce(ctx, "old", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		t.Fatal("legacy retry reselected")
		return selection, nil
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil })
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatal(got, err)
	}
	assertActivationCatalogUnchanged(t, path, before)
	// A new first-activation candidate can claim schema7 alongside the legacy
	// receipt without rewriting its historical serialized contract.
	d := sample()
	d.Key = state.Key
	d.Steps = []string{"third unique candidate"}
	v, err := s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateAt(ctx, state, v.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	next, err := s.ActivationState(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	p := selection.Policy
	p.Comparison.BaselineVersion = state.Active
	p.Comparison.CandidateVersion = v.ID
	if _, err = s.OutcomeRollbackOnce(ctx, "new", next, p, func(context.Context) (ComparisonSelectionReport, error) {
		return ComparisonSelectionReport{Version: 1, Policy: p}, nil
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil }); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.OutcomeRollbackOperation(ctx, state.Key, "old")
	if err != nil || !reflect.DeepEqual(legacy, r) {
		t.Fatal("legacy receipt changed", legacy, err)
	}
	if err = s.read("catalog.json", &c); err != nil || c.Schema != 7 || len(c.OutcomeIntents) != 1 || len(c.OutcomeOperations) != 2 {
		t.Fatal(c.Schema, err)
	}
}

func TestOutcomeIntentCorruptionAndPendingPrefix(t *testing.T) {
	for _, mode := range []string{"missing", "marker", "model", "pending-prefix", "pending-key", "receipt-prefix"} {
		t.Run(mode, func(t *testing.T) {
			s, path, state, selection := outcomeRollbackFixture(t, false)
			ctx := context.Background()
			_, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
				if mode == "pending-prefix" || mode == "pending-key" {
					return ComparisonSelectionReport{}, ErrInvalid
				}
				return selection, nil
			}, func(context.Context, OutcomeRollbackReceipt) error { return nil })
			if mode != "pending-prefix" && mode != "pending-key" && err != nil {
				t.Fatal(err)
			}
			var c catalog
			if mode == "pending-key" {
				d := sample()
				d.Key = state.Key
				d.Key.Name = "other"
				a, e := s.Draft(ctx, d, false)
				if e != nil {
					t.Fatal(e)
				}
				if e = s.Activate(ctx, d.Key, a.ID, "", pass, false); e != nil {
					t.Fatal(e)
				}
				d.Steps = []string{"other candidate"}
				b, e := s.Draft(ctx, d, false)
				if e != nil {
					t.Fatal(e)
				}
				if e = s.Activate(ctx, d.Key, b.ID, a.ID, pass, false); e != nil {
					t.Fatal(e)
				}
			}
			if err = s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				delete(c.OutcomeIntents, "op")
			case "marker":
				r := c.OutcomeOperations["op"]
				r.IntentID = "other"
				c.OutcomeOperations["op"] = r
			case "model":
				i := c.OutcomeIntents["op"]
				i.ConfiguredModelID = "other"
				c.OutcomeIntents["op"] = i
			case "pending-prefix":
				i := c.OutcomeIntents["op"]
				i.Expected.Revision = strings.Repeat("f", 64)
				c.OutcomeIntents["op"] = i
			case "pending-key":
				i := c.OutcomeIntents["op"]
				i.Expected.Key.Name = "other"
				i.Policy.Comparison.Key = i.Expected.Key
				c.OutcomeIntents["op"] = i
			case "receipt-prefix":
				r := c.OutcomeOperations["op"]
				r.IntentID = ""
				r.Expected.Revision = strings.Repeat("f", 64)
				r.After = r.Expected
				c.OutcomeOperations["op"] = r
				c.OutcomeIntents = nil
				c.Schema = 6
			}
			if err = s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			ro, err := OpenReadOnly(path, []string{state.Key.Scope})
			if mode == "pending-prefix" || mode == "pending-key" || mode == "receipt-prefix" {
				if err != nil {
					t.Fatal(err)
				}
				defer ro.Close()
				if _, err = ro.OutcomeRollbackIntent(ctx, state.Key, "op"); err == nil {
					t.Fatal("forged prefix accepted")
				}
				calls := 0
				if _, err = s.OutcomeRollbackOnce(ctx, "alternate", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }); err == nil || calls != 0 {
					t.Fatal("forged pending revision bypassed", err, calls)
				}
			} else if err == nil {
				ro.Close()
				t.Fatal("corrupt intent linkage accepted")
			}
		})
	}
}

func TestOutcomeIntentConcurrentExactIDDoesNotReselect(t *testing.T) {
	s, _, state, selection := outcomeRollbackFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	guard := func(context.Context, OutcomeRollbackReceipt) error { return nil }
	go func() {
		_, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, func(ctx context.Context) (ComparisonSelectionReport, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ComparisonSelectionReport{}, ctx.Err()
			}
			return selection, nil
		}, guard)
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("selector not entered")
	}
	if _, err := s.OutcomeRollbackOnce(ctx, "op", state, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		t.Error("duplicate selector")
		return selection, nil
	}, guard); !errors.Is(err, ErrOutcomePending) {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("selector did not finish")
	}
}
