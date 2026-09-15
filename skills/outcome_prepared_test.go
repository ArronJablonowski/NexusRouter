package skills

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestOutcomeRollbackPreparedPersistsIntentAndSelectionAtomically(t *testing.T) {
	store, path, expected, report := outcomeRollbackFixture(t, true)
	ctx := context.Background()
	intentCalls, selectionCalls := 0, 0
	_, err := store.OutcomeRollbackPrepared(ctx, "prepared", "", expected, report.Policy, report,
		func(context.Context, OutcomeRollbackReceipt) error { return ErrInvalid },
		func(_ context.Context, intent OutcomeRollbackIntent) error {
			intentCalls++
			if intent.Expected != expected {
				t.Fatal("wrong intent binding", intent)
			}
			return nil
		},
		func(_ context.Context, checkpoint OutcomeSelectionCheckpoint) error {
			selectionCalls++
			if !reflect.DeepEqual(checkpoint.Report, report) {
				t.Fatal("selected evidence changed")
			}
			return nil
		})
	if err == nil || intentCalls != 1 || selectionCalls < 1 {
		t.Fatal(err, intentCalls, selectionCalls)
	}
	intent, intentErr := store.OutcomeRollbackIntent(ctx, expected.Key, "prepared")
	checkpoint, checkpointErr := store.OutcomeSelectionCheckpoint(ctx, expected.Key, "prepared")
	if intentErr != nil || checkpointErr != nil || checkpoint.Intent != intent || !reflect.DeepEqual(checkpoint.Report, report) {
		t.Fatal(intent, intentErr, checkpoint, checkpointErr)
	}
	if _, lookupErr := store.OutcomeRollbackOperation(ctx, expected.Key, "prepared"); !errors.Is(lookupErr, ErrNotFound) {
		t.Fatal("failed final guard created receipt", lookupErr)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reopened, openErr := Open(path, []string{expected.Key.Scope})
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer reopened.Close()
	reopened.SetAutomatic(true)
	reopened.SetOutcomeRollback(true)
	resumedIntentCalls, resumedSelectionCalls := 0, 0
	receipt, err := reopened.OutcomeRollbackPrepared(ctx, "prepared", "", expected, report.Policy, report,
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { resumedIntentCalls++; return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { resumedSelectionCalls++; return nil })
	if err != nil || receipt.Decision != "rolled_back" || receipt.SelectionID != "prepared" ||
		resumedIntentCalls != 0 || resumedSelectionCalls < 1 {
		t.Fatal(receipt, err, resumedIntentCalls, resumedSelectionCalls)
	}
}

func TestOutcomeRollbackPreparedGuardsBeforeAtomicPersistence(t *testing.T) {
	for _, reject := range []string{"intent", "selection"} {
		t.Run(reject, func(t *testing.T) {
			store, path, expected, report := outcomeRollbackFixture(t, true)
			ctx := context.Background()
			intentGuard := func(context.Context, OutcomeRollbackIntent) error { return nil }
			selectionGuard := func(context.Context, OutcomeSelectionCheckpoint) error { return nil }
			if reject == "intent" {
				intentGuard = func(context.Context, OutcomeRollbackIntent) error { return ErrInvalid }
			} else {
				selectionGuard = func(context.Context, OutcomeSelectionCheckpoint) error { return ErrInvalid }
			}
			if _, err := store.OutcomeRollbackPrepared(ctx, "prepared", "", expected, report.Policy, report,
				func(context.Context, OutcomeRollbackReceipt) error { return nil }, intentGuard, selectionGuard); err == nil {
				t.Fatal("guard rejection ignored")
			}
			if _, err := store.OutcomeRollbackIntent(ctx, expected.Key, "prepared"); !errors.Is(err, ErrNotFound) {
				t.Fatal("intent escaped rejected atomic prepare", err)
			}
			if _, err := store.OutcomeSelectionCheckpoint(ctx, expected.Key, "prepared"); !errors.Is(err, ErrNotFound) {
				t.Fatal("checkpoint escaped rejected atomic prepare", err)
			}
			assertActivationCatalogUnchanged(t, path, activationCatalogBytes(t, path))
		})
	}
}

func TestOutcomeRollbackPreparedRejectsInsufficientEvidenceBeforeClaim(t *testing.T) {
	store, _, expected, report := outcomeRollbackFixture(t, true)
	report.Comparison.Baseline.Samples = report.Policy.Comparison.MinSamples - 1
	report.Comparison.Baseline.Accepted = report.Comparison.Baseline.Samples
	report.Comparison.Excluded["unknown_quality"] = 1
	setComparisonInterval(&report.Comparison.Baseline)
	report.Comparison.Status = comparisonStatus(*report.Comparison)
	if report.Validate() != nil {
		t.Fatal("invalid insufficient fixture")
	}
	if _, err := store.OutcomeRollbackPrepared(context.Background(), "premature", "", expected, report.Policy, report,
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { return nil }); err == nil {
		t.Fatal("insufficient evidence claimed")
	}
	if _, err := store.OutcomeRollbackIntent(context.Background(), expected.Key, "premature"); !errors.Is(err, ErrNotFound) {
		t.Fatal("insufficient evidence persisted intent", err)
	}
}
