package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func withdrawn(r evaluation.Record, id string) evaluation.Record {
	r.ID = id
	r.AllowJudge = false
	r.Checks = []evaluation.Check{{Source: evaluation.Withdrawn, Reference: id}}
	return r
}

func TestWithdrawalPreservesHistoryAndRemovesRoutingContribution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "withdraw.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := revisionBase(t, db)
	next := withdrawn(base, "withdraw")
	if err := db.SupersedeEvaluation(ctx, base.ID, next); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeEvaluation(ctx, base.ID, next); err != nil {
		t.Fatal("idempotent retry", err)
	}
	for _, direct := range []bool{false, true} {
		set, err := db.ObservationSet(ctx, base.Key)
		if direct {
			set, err = db.DirectObservationSet(ctx, base.Key)
		}
		if err != nil || len(set.Fitness) != 0 {
			t.Fatal("withdrawn family still influences routing", set, err)
		}
	}
	if _, err := db.Fitness(ctx, base.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("withdrawn sample remains in lifetime fitness", err)
	}
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTraceFitnessProjection(ctx, tx, next); err != nil {
		t.Fatal("withdrawn trace unavailable", err)
	}
	if err := validateTraceFitnessProjection(ctx, tx, base); err == nil {
		t.Fatal("zero-sample corruption accepted for an active evaluation")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeEvaluation(ctx, base.ID, revised(base, "stale", true)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale correction admitted", err)
	}
	if err := db.RecordEvaluation(ctx, withdrawn(base, "new-base")); !errors.Is(err, evaluation.ErrEvidence) {
		t.Fatal("withdrawal without prior admitted", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := db.EvaluationHistory(ctx, base.TaskID, base.AttemptID)
	if err != nil || len(history) != 2 || history[0].ID != base.ID || history[1].ID != next.ID {
		t.Fatal(history, err)
	}
	restore := revised(next, "restore", true)
	if err := db.SupersedeEvaluation(ctx, next.ID, restore); err != nil {
		t.Fatal(err)
	}
	fitness, err := db.Fitness(ctx, base.Key)
	if err != nil || fitness.Samples != 1 || fitness.Quality != 1 || fitness.Latency != base.Latency || fitness.Cost != base.Cost {
		t.Fatal("restoration duplicated or changed measurements", fitness, err)
	}
	set, err := db.DirectObservationSet(ctx, base.Key)
	if err != nil || len(set.Fitness) != 3 {
		t.Fatal("restored correction family missing", set, err)
	}
}

func TestWithdrawalRollsBackWhenProjectionWriteFails(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	if _, err := db.db.Exec(`CREATE TRIGGER refuse_withdrawal BEFORE UPDATE ON fitness BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeEvaluation(ctx, base.ID, withdrawn(base, "withdraw")); err == nil {
		t.Fatal("write failure ignored")
	}
	history, err := db.EvaluationHistory(ctx, base.TaskID, base.AttemptID)
	if err != nil || len(history) != 1 {
		t.Fatal("partial withdrawal", history, err)
	}
}

func TestWithdrawalExcludesContextQualityButPreservesSafetyMeasurements(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := contextTestRecord(t, db, "task")
	base.PeakMemoryBytes = 12345
	base.SwapGrowthBytes = 678
	if err := db.RecordEvaluation(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeEvaluation(ctx, base.ID, withdrawn(base, "withdraw")); err != nil {
		t.Fatal(err)
	}
	evidence, err := db.ContextEvidenceFor(ctx, base.Key)
	if err != nil || len(evidence) != 1 || evidence[0].Samples != 0 || evidence[0].Quality != 0 || evidence[0].PeakMemory != 12345 || evidence[0].MaxSwapGrowth != 678 {
		t.Fatal(evidence, err)
	}
}

func TestWithdrawalDoesNotProduceSkillQuality(t *testing.T) {
	db, _ := generationStore(t)
	ctx := context.Background()
	base := workflowSourceFixture(t, db, "withdraw-skill", "session", "creative", runtime.TaskCompleted, evaluation.UserFeedback, true, true)
	if err := db.SupersedeEvaluation(ctx, base.ID, withdrawn(base, "withdraw")); err != nil {
		t.Fatal(err)
	}
	out, err := db.SkillTaskOutcome(ctx, base.TaskID)
	if err != nil || out.Quality != nil || out.EvaluationID != "" || out.EvaluationDigest != "" {
		t.Fatal(out, err)
	}
}
