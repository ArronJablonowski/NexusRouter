package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func settledNoActionFixture(t *testing.T) (*FileStore, string, ActivationState, ComparisonSelectionReport, OutcomeRollbackReceipt, ActivationState) {
	t.Helper()
	store, path, candidate, report := outcomeRollbackFixture(t, false)
	receipt, err := store.OutcomeRollbackOnceCheckpointed(context.Background(), "settled", "", candidate, report.Policy,
		func(context.Context) (ComparisonSelectionReport, error) { return report, nil },
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || receipt.Decision != "no_action" || receipt.SelectionID != "settled" {
		t.Fatal(receipt, err)
	}
	if err = store.RollbackAt(context.Background(), candidate, false); err != nil {
		t.Fatal(err)
	}
	current, err := store.ActivationState(context.Background(), candidate.Key)
	if err != nil || current.Revision == candidate.Revision {
		t.Fatal("rollback did not advance activation revision", current, err)
	}
	return store, path, candidate, report, receipt, current
}

func TestOutcomeRetentionSettlesHistoricalNoActionAndFencesReuse(t *testing.T) {
	store, _, candidate, report, receipt, current := settledNoActionFixture(t)
	ctx := context.Background()
	wantDigest, err := outcomeReceiptDigest(receipt)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := store.PruneSettledOutcomeHistory(ctx, OutcomeRetentionRequest{
		Version: 1, Expected: current, Before: receipt.CheckedAt.Add(time.Nanosecond), Limit: 1,
	})
	if err != nil || len(settled) != 1 || settled[0].OperationID != "settled" ||
		settled[0].Expected != candidate || settled[0].ReceiptDigest != wantDigest || settled[0].Validate() != nil {
		t.Fatal(settled, err)
	}
	if _, err = store.OutcomeRollbackOperation(ctx, candidate.Key, "settled"); !errors.Is(err, ErrNotFound) {
		t.Fatal("retired full receipt remained visible", err)
	}
	tombstone, err := store.OutcomeSettlement(ctx, candidate.Key, "settled")
	if err != nil || !reflect.DeepEqual(tombstone, settled[0]) {
		t.Fatal(tombstone, err)
	}
	var catalog catalog
	if err = store.read("catalog.json", &catalog); err != nil || catalog.Schema != 10 ||
		len(catalog.OutcomeOperations) != 0 || len(catalog.OutcomeIntents) != 0 ||
		len(catalog.OutcomeSelections) != 0 || len(catalog.OutcomeSettlements) != 1 {
		t.Fatal(catalog.Schema, err)
	}

	// Reactivation appends history and therefore has a distinct revision, but a
	// caller still cannot recycle the settled operation identity.
	if err = store.ActivateAt(ctx, current, candidate.Active, pass, false); err != nil {
		t.Fatal(err)
	}
	reactivated, err := store.ActivationState(ctx, candidate.Key)
	if err != nil || reactivated.Revision == candidate.Revision || reactivated.Revision == current.Revision {
		t.Fatal("reactivation reused an activation revision", reactivated, err)
	}
	calls := 0
	_, err = store.OutcomeRollbackOnce(ctx, "settled", reactivated, report.Policy,
		func(context.Context) (ComparisonSelectionReport, error) { calls++; return report, nil },
		func(context.Context, OutcomeRollbackReceipt) error { return nil })
	if !errors.Is(err, ErrConflict) || calls != 0 {
		t.Fatal("settled operation identity was reusable", err, calls)
	}
}

func TestOutcomeRetentionEligibilityAndCAS(t *testing.T) {
	t.Run("current revision and exclusive cutoff", func(t *testing.T) {
		store, path, candidate, report := outcomeRollbackFixture(t, false)
		receipt, err := store.OutcomeRollbackOnceCheckpointed(context.Background(), "current", "", candidate, report.Policy,
			func(context.Context) (ComparisonSelectionReport, error) { return report, nil },
			func(context.Context, OutcomeRollbackReceipt) error { return nil },
			func(context.Context, OutcomeRollbackIntent) error { return nil },
			func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		before := activationCatalogBytes(t, path)
		for _, cutoff := range []time.Time{receipt.CheckedAt, receipt.CheckedAt.Add(time.Hour)} {
			got, pruneErr := store.PruneSettledOutcomeHistory(context.Background(), OutcomeRetentionRequest{Version: 1, Expected: candidate, Before: cutoff, Limit: 1})
			if pruneErr != nil || len(got) != 0 {
				t.Fatal(got, pruneErr)
			}
		}
		assertActivationCatalogUnchanged(t, path, before)
	})

	t.Run("stale caller state", func(t *testing.T) {
		store, path, candidate, _, receipt, current := settledNoActionFixture(t)
		draft := sample()
		draft.Key = candidate.Key
		draft.Steps = []string{"new retention CAS candidate"}
		version, err := store.Draft(context.Background(), draft, false)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.ActivateAt(context.Background(), current, version.ID, pass, false); err != nil {
			t.Fatal(err)
		}
		before := activationCatalogBytes(t, path)
		_, err = store.PruneSettledOutcomeHistory(context.Background(), OutcomeRetentionRequest{Version: 1, Expected: current, Before: receipt.CheckedAt.Add(time.Hour), Limit: 1})
		if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		assertActivationCatalogUnchanged(t, path, before)
	})
}

func TestOutcomeRetentionUsesOldestFirstAndHonorsLimit(t *testing.T) {
	store, _, firstState, firstReport, firstReceipt, current := settledNoActionFixture(t)
	ctx := context.Background()
	draft := sample()
	draft.Key = firstState.Key
	draft.Steps = []string{"second unique retention candidate"}
	version, err := store.Draft(ctx, draft, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ActivateAt(ctx, current, version.ID, pass, false); err != nil {
		t.Fatal(err)
	}
	secondState, err := store.ActivationState(ctx, firstState.Key)
	if err != nil {
		t.Fatal(err)
	}
	secondReport := firstReport
	secondReport.Policy.Comparison.BaselineVersion = current.Active
	secondReport.Policy.Comparison.CandidateVersion = version.ID
	comparison := *firstReport.Comparison
	comparison.Policy = secondReport.Policy.Comparison
	comparison.Baseline.Version = current.Active
	comparison.Candidate.Version = version.ID
	if err = store.with(ctx, func(c *catalog) error {
		for _, metadata := range c.Skills[firstState.Key.index()].Versions {
			if metadata.Version == version.ID {
				comparison.Candidate.Digest = metadata.Digest
			}
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	secondReport.Comparison = &comparison
	if secondReport.Validate() != nil {
		t.Fatal("second selection fixture invalid")
	}
	secondReceipt, err := store.OutcomeRollbackOnceCheckpointed(ctx, "zzz-newer", "", secondState, secondReport.Policy,
		func(context.Context) (ComparisonSelectionReport, error) { return secondReport, nil },
		func(context.Context, OutcomeRollbackReceipt) error { return nil },
		func(context.Context, OutcomeRollbackIntent) error { return nil },
		func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || secondReceipt.CheckedAt.Before(firstReceipt.CheckedAt) {
		t.Fatal(firstReceipt.CheckedAt, secondReceipt.CheckedAt, err)
	}
	if err = store.RollbackAt(ctx, secondState, false); err != nil {
		t.Fatal(err)
	}
	current, err = store.ActivationState(ctx, firstState.Key)
	if err != nil {
		t.Fatal(err)
	}

	settled, err := store.PruneSettledOutcomeHistory(ctx, OutcomeRetentionRequest{
		Version: 1, Expected: current, Before: secondReceipt.CheckedAt.Add(time.Nanosecond), Limit: 1,
	})
	if err != nil || len(settled) != 1 || settled[0].OperationID != "settled" {
		t.Fatal("retention did not choose oldest receipt", settled, err)
	}
	if _, err = store.OutcomeRollbackOperation(ctx, firstState.Key, "zzz-newer"); err != nil {
		t.Fatal("limit removed newer receipt", err)
	}
	if _, err = store.OutcomeSettlement(ctx, firstState.Key, "zzz-newer"); !errors.Is(err, ErrNotFound) {
		t.Fatal("newer receipt unexpectedly settled", err)
	}
}

func TestOutcomeRetentionNeverPrunesRollbackOrPending(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		store, path, candidate, report := outcomeRollbackFixture(t, true)
		receipt, err := store.OutcomeRollbackOnceCheckpointed(context.Background(), "rollback", "", candidate, report.Policy,
			func(context.Context) (ComparisonSelectionReport, error) { return report, nil },
			func(context.Context, OutcomeRollbackReceipt) error { return nil },
			func(context.Context, OutcomeRollbackIntent) error { return nil },
			func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
		if err != nil || receipt.Decision != "rolled_back" {
			t.Fatal(receipt, err)
		}
		before := activationCatalogBytes(t, path)
		got, err := store.PruneSettledOutcomeHistory(context.Background(), OutcomeRetentionRequest{Version: 1, Expected: receipt.After, Before: receipt.CheckedAt.Add(time.Hour), Limit: 1})
		if err != nil || len(got) != 0 {
			t.Fatal(got, err)
		}
		assertActivationCatalogUnchanged(t, path, before)
	})

	t.Run("pending", func(t *testing.T) {
		store, path, candidate, report := outcomeRollbackFixture(t, false)
		_, err := store.OutcomeRollbackOnceCheckpointed(context.Background(), "pending", "", candidate, report.Policy,
			func(context.Context) (ComparisonSelectionReport, error) {
				return ComparisonSelectionReport{}, ErrInvalid
			},
			func(context.Context, OutcomeRollbackReceipt) error { return nil },
			func(context.Context, OutcomeRollbackIntent) error { return nil },
			func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
		if err == nil {
			t.Fatal("selector failure accepted")
		}
		if err = store.RollbackAt(context.Background(), candidate, false); err != nil {
			t.Fatal(err)
		}
		current, err := store.ActivationState(context.Background(), candidate.Key)
		if err != nil {
			t.Fatal(err)
		}
		before := activationCatalogBytes(t, path)
		got, err := store.PruneSettledOutcomeHistory(context.Background(), OutcomeRetentionRequest{Version: 1, Expected: current, Before: time.Now().UTC().Add(time.Hour), Limit: 1})
		if err != nil || len(got) != 0 {
			t.Fatal(got, err)
		}
		if _, err = store.OutcomeRollbackIntent(context.Background(), candidate.Key, "pending"); err != nil {
			t.Fatal("pending intent was removed", err)
		}
		assertActivationCatalogUnchanged(t, path, before)
	})
}

func TestOutcomeRetentionRejectsDeepCorruptionBeforeMutation(t *testing.T) {
	store, path, candidate, _, receipt, current := settledNoActionFixture(t)
	var catalog catalog
	if err := store.read("catalog.json", &catalog); err != nil {
		t.Fatal(err)
	}
	operation := catalog.OutcomeOperations["settled"]
	checkpoint := catalog.OutcomeSelections["settled"]
	operation.Selection.Comparison.Candidate.Digest = strings.Repeat("f", 64)
	checkpoint.Report.Comparison.Candidate.Digest = strings.Repeat("f", 64)
	catalog.OutcomeOperations["settled"] = operation
	catalog.OutcomeSelections["settled"] = checkpoint
	if err := store.write("catalog.json", catalog, false); err != nil {
		t.Fatal(err)
	}
	before := activationCatalogBytes(t, path)
	_, err := store.PruneSettledOutcomeHistory(context.Background(), OutcomeRetentionRequest{Version: 1, Expected: current, Before: receipt.CheckedAt.Add(time.Hour), Limit: 1})
	if err == nil {
		t.Fatal("corrupt selected evidence was pruned")
	}
	assertActivationCatalogUnchanged(t, path, before)
	if _, err = store.OutcomeSettlement(context.Background(), candidate.Key, "settled"); !errors.Is(err, ErrNotFound) {
		t.Fatal("corruption produced a settlement", err)
	}
}
