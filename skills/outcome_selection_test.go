package skills

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func checkpointCall(s *FileStore, ctx context.Context, id string, state ActivationState, report ComparisonSelectionReport, selector OutcomeSelector, receiptGuard OutcomeRollbackGuard, selectionGuard OutcomeSelectionGuard) (OutcomeRollbackReceipt, error) {
	return s.OutcomeRollbackOnceCheckpointed(ctx, id, "", state, report.Policy, selector, receiptGuard, func(context.Context, OutcomeRollbackIntent) error { return nil }, selectionGuard)
}

func TestOutcomeSelectionFrozenResumeAndReceipt(t *testing.T) {
	for _, signal := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_action", true: "rollback"}[signal], func(t *testing.T) {
			s, path, state, report := outcomeRollbackFixture(t, signal)
			ctx := context.Background()
			calls := 0
			selector := func(context.Context) (ComparisonSelectionReport, error) { calls++; return report, nil }
			selectionGuard := func(_ context.Context, c OutcomeSelectionCheckpoint) error {
				if c.Validate() != nil {
					t.Fatal(c)
				}
				c.Report.Comparison.Excluded["invented"] = 1
				return nil
			}
			if r, err := checkpointCall(s, ctx, "op", state, report, selector, func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, selectionGuard); err == nil || r.Version != 0 {
				t.Fatal(r, err)
			}
			checkpoint, err := s.OutcomeSelectionCheckpoint(ctx, state.Key, "op")
			if err != nil || checkpoint.Validate() != nil || len(checkpoint.Report.Comparison.Excluded) != 0 {
				t.Fatal(checkpoint, err)
			}
			if _, err = s.OutcomeRollbackOperation(ctx, state.Key, "op"); !errors.Is(err, ErrNotFound) {
				t.Fatal("checkpoint was called completed", err)
			}
			current, err := s.ActivationState(ctx, state.Key)
			if err != nil || current != state {
				t.Fatal(current, err)
			}
			// Callback-owned objects and later evidence must not change the snapshot.
			report.Comparison.Excluded["changed"] = 999
			reopened, err := Open(path, []string{state.Key.Scope})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.SetAutomatic(true)
			reopened.SetOutcomeRollback(true)
			out, err := checkpointCall(reopened, ctx, "op", state, checkpoint.Report, func(context.Context) (ComparisonSelectionReport, error) {
				t.Fatal("checkpoint reselected")
				return ComparisonSelectionReport{}, ErrInvalid
			}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
			if err != nil || out.Validate() != nil || out.SelectionID != "op" || !reflect.DeepEqual(out.Selection, checkpoint.Report) || calls != 1 {
				t.Fatal(out, err, calls)
			}
			saved, err := reopened.OutcomeSelectionCheckpoint(ctx, state.Key, "op")
			if err != nil || !reflect.DeepEqual(saved, checkpoint) {
				t.Fatal("snapshot changed", saved, err)
			}
			var c catalog
			if err = s.read("catalog.json", &c); err != nil || c.Schema != 8 {
				t.Fatal(c.Schema, err)
			}
		})
	}
}

func TestOutcomeSelectionGuardRejectsBeforePersistence(t *testing.T) {
	for _, mode := range []string{"deny", "panic", "cancel", "kill"} {
		t.Run(mode, func(t *testing.T) {
			s, _, state, report := outcomeRollbackFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			guard := func(context.Context, OutcomeSelectionCheckpoint) error {
				switch mode {
				case "deny":
					return ErrInvalid
				case "panic":
					panic("private")
				case "cancel":
					cancel()
				case "kill":
					s.SetOutcomeRollback(false)
				}
				return nil
			}
			if _, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { calls++; return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { t.Fatal("final guard called"); return nil }, guard); err == nil {
				t.Fatal("selection guard bypassed")
			}
			ctx = context.Background()
			s.SetOutcomeRollback(true)
			if _, err := s.OutcomeSelectionCheckpoint(ctx, state.Key, "op"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if _, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { calls++; return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil }); !errors.Is(err, ErrOutcomePending) || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}

func TestOutcomeSelectionResumeRequiresBothImmutableFiles(t *testing.T) {
	for _, which := range []string{"baseline", "candidate", "corrupt-baseline", "corrupt-candidate"} {
		t.Run(which, func(t *testing.T) {
			s, path, state, report := outcomeRollbackFixture(t, true)
			ctx := context.Background()
			if _, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil }); err == nil {
				t.Fatal("guard failed")
			}
			version := report.Policy.Comparison.BaselineVersion
			if which == "candidate" || which == "corrupt-candidate" {
				version = state.Active
			}
			var damage error
			if which == "corrupt-baseline" || which == "corrupt-candidate" {
				damage = s.write("version-"+version+".json", Version{}, false)
			} else {
				damage = s.root.Remove("version-" + version + ".json")
			}
			if damage != nil {
				t.Fatal(damage)
			}
			before := activationCatalogBytes(t, path)
			calls := 0
			if _, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { calls++; return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil }); err == nil || calls != 0 {
				t.Fatal("missing body rollback", err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestOutcomeSelectionLegacyGuardCannotResume(t *testing.T) {
	s, path, state, report := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	_, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err == nil {
		t.Fatal("expected final denial")
	}
	before := activationCatalogBytes(t, path)
	_, err = s.OutcomeRollbackOnce(ctx, "op", state, report.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		t.Fatal("selector called")
		return report, nil
	}, func(context.Context, OutcomeRollbackReceipt) error { t.Fatal("receipt guard called"); return nil })
	if !errors.Is(err, ErrOutcomePending) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestOutcomeSelectionConcurrentSavedResume(t *testing.T) {
	s, _, state, report := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	_, _ = checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	var wg sync.WaitGroup
	results := make(chan OutcomeRollbackReceipt, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { t.Error("reselected"); return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	a, b := <-results, <-results
	if a.Version != 1 || !reflect.DeepEqual(a, b) {
		t.Fatal(a, b)
	}
}

func TestOutcomeSelectionCorruptionFailsClosed(t *testing.T) {
	for _, mode := range []string{"report", "map-key", "missing-intent", "missing-checkpoint", "receipt-report"} {
		t.Run(mode, func(t *testing.T) {
			s, path, state, report := outcomeRollbackFixture(t, false)
			ctx := context.Background()
			_, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error {
				if mode == "missing-checkpoint" || mode == "receipt-report" {
					return nil
				}
				return ErrInvalid
			}, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
			if mode == "missing-checkpoint" || mode == "receipt-report" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected denial")
			}
			var c catalog
			if err = s.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "report":
				cp := c.OutcomeSelections["op"]
				cp.Report.ConfiguredModelID = "other"
				c.OutcomeSelections["op"] = cp
			case "map-key":
				c.OutcomeSelections["other"] = c.OutcomeSelections["op"]
				delete(c.OutcomeSelections, "op")
			case "missing-intent":
				delete(c.OutcomeIntents, "op")
			case "missing-checkpoint":
				delete(c.OutcomeSelections, "op")
			case "receipt-report":
				r := c.OutcomeOperations["op"]
				r.Selection.Watermark++
				c.OutcomeOperations["op"] = r
			}
			if err = s.write("catalog.json", c, false); err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			for _, id := range []string{"op", "new"} {
				if _, err = checkpointCall(s, ctx, id, state, report, func(context.Context) (ComparisonSelectionReport, error) {
					t.Fatal("corruption reselected")
					return report, nil
				}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil }); err == nil {
					t.Fatal("corruption accepted")
				}
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestOutcomeSelectionResumeRequiresCurrentCAS(t *testing.T) {
	s, path, state, report := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	_, err := checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err == nil {
		t.Fatal("expected denial")
	}
	if err = s.RollbackAt(ctx, state, false); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	_, err = checkpointCall(s, ctx, "op", state, report, func(context.Context) (ComparisonSelectionReport, error) {
		t.Fatal("reselected stale state")
		return report, nil
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestOutcomeSelectionLegacyReceiptsRemainHistorical(t *testing.T) {
	for _, schema := range []int{6, 7} {
		t.Run(map[int]string{6: "six", 7: "seven"}[schema], func(t *testing.T) {
			s, path, state, report := outcomeRollbackFixture(t, false)
			ctx := context.Background()
			r, err := s.OutcomeRollbackOnce(ctx, "old", state, report.Policy, func(context.Context) (ComparisonSelectionReport, error) { return report, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if schema == 6 {
				var c catalog
				if err = s.read("catalog.json", &c); err != nil {
					t.Fatal(err)
				}
				r.IntentID = ""
				c.OutcomeOperations["old"] = r
				c.OutcomeIntents = nil
				c.Schema = 6
				if err = s.write("catalog.json", c, false); err != nil {
					t.Fatal(err)
				}
			}
			before := activationCatalogBytes(t, path)
			out, err := checkpointCall(s, ctx, "old", state, report, func(context.Context) (ComparisonSelectionReport, error) {
				t.Fatal("legacy reselected")
				return report, nil
			}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error {
				t.Fatal("legacy checkpoint fabricated")
				return nil
			})
			if err != nil || !reflect.DeepEqual(out, r) {
				t.Fatal(out, err)
			}
			if _, err = s.OutcomeSelectionCheckpoint(ctx, state.Key, "old"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}
