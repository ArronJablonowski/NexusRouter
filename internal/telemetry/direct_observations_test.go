package telemetry

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func TestDirectObservationSetUsesCurrentVerdictWithoutDroppingCorrectionHistory(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "priors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	set, err := db.DirectObservationSet(ctx, base.Key)
	if err != nil || len(set.Fitness) != 0 || len(set.Advisory) != 0 {
		t.Fatal("judge-only prior included", set, err)
	}
	original, err := db.ObservationSet(ctx, base.Key, false)
	if err != nil || len(original.Fitness) != 1 {
		t.Fatal("in-category judge evidence changed", original, err)
	}
	correction := revised(base, "operator", false)
	if err = db.SupersedeEvaluation(ctx, base.ID, correction); err != nil {
		t.Fatal(err)
	}
	set, err = db.DirectObservationSet(ctx, base.Key)
	if err != nil || len(set.Fitness) != 2 {
		t.Fatal("correction history missing", set, err)
	}
	prior, err := routing.AggregateEvidence(base.Key, set, base.Time, routing.Defaults())
	if err != nil || prior.Samples != 1 || prior.Quality != 0 || !prior.Updated.Equal(base.Time) {
		t.Fatal("correction double-counted or retimed", prior, err)
	}
}

func TestDirectObservationSetValidatesJudgeFamiliesBeforeExcluding(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "invalid-prior.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := revisionBase(t, db)
	if _, err = db.db.ExecContext(ctx, "UPDATE evaluation_heads SET current_id='missing' WHERE base_id=?", base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DirectObservationSet(ctx, base.Key); err == nil {
		t.Fatal("filtering judge verdict bypassed corruption check")
	}
}
