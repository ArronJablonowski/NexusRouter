package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestSDKOutcomeCheckpointRejectsRevisedFeedback(t *testing.T) {
	ctx := context.Background()
	client, _, database, root, expected, request := sdkOutcomeRollbackEvidence(t)
	report, err := client.SelectSkillComparison(ctx, request)
	if err != nil || report.Validate() != nil || report.Comparison == nil || report.Comparison.Status != "regression_signal" || report.Sources == nil || len(report.Sources.Tasks) != 40 || report.Sources.Tasks[0] != "task-00" || report.Sources.Tasks[39] != "task-39" {
		t.Fatal("selection fixture unavailable", err)
	}
	store, err := skills.Open(root, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	stop := errors.New("fixture pauses final commit")
	_, err = store.OutcomeRollbackOnceCheckpointed(ctx, "freshness-operation", request.ModelID, expected, report.Policy, func(context.Context) (skills.ComparisonSelectionReport, error) { return report, nil }, func(context.Context, skills.OutcomeRollbackReceipt) error { return stop }, func(context.Context, skills.OutcomeRollbackIntent) error { return nil }, func(context.Context, skills.OutcomeSelectionCheckpoint) error { return nil })
	if !errors.Is(err, stop) {
		t.Fatal("fixture failed before selected checkpoint", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := client.OutcomeSelectionCheckpoint(ctx, expected.Key, "freshness-operation")
	if err != nil || checkpoint.Validate() != nil || !reflect.DeepEqual(checkpoint.Report, report) {
		t.Fatal("selected checkpoint not preserved", err)
	}
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := db.CurrentEvaluation(ctx, "task-00", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	next := prior
	next.ID = "revised-feedback"
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user-correction", Passed: false}}
	if err := db.SupersedeEvaluation(ctx, prior.ID, next); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	catalogBefore, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := client.OutcomeRollbackOnce(ctx, "freshness-operation", expected, request); err == nil || receipt.Version != 0 {
		t.Fatal("revised feedback authorized stale rollback")
	}
	if _, err := client.OutcomeRollbackOperation(ctx, expected.Key, "freshness-operation"); err == nil {
		t.Fatal("stale evidence produced receipt")
	}
	again, err := client.OutcomeSelectionCheckpoint(ctx, expected.Key, "freshness-operation")
	if err != nil || !reflect.DeepEqual(again, checkpoint) {
		t.Fatal("stale evidence inspection rewritten", err)
	}
	state, err := client.SkillActivationState(ctx, expected.Key)
	if err != nil || state != expected {
		t.Fatal("stale evidence changed activation", err)
	}
	catalogAfter, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil || string(catalogBefore) != string(catalogAfter) {
		t.Fatal("stale retry changed catalog", err)
	}
	databaseAfter, err := os.ReadFile(database)
	if err != nil || string(databaseBefore) != string(databaseAfter) {
		t.Fatal("freshness inspection mutated evidence", err)
	}
}
