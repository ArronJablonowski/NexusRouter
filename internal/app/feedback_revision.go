package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"darwinrouter/evaluation"
	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

func FeedbackHistory(ctx context.Context, path, task string) ([]evaluation.Record, error) {
	if task == "" || len(task) > 128 {
		return nil, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	snapshot, err := sessions.Replay(ctx, db, task)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || snapshot.InterruptedTurn {
		return nil, ErrAdmission
	}
	var seq int64
	attempt := ""
	for page := 0; page < 1000; page++ {
		events, err := db.Read(ctx, task, seq, 256)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			seq = e.Sequence
			if e.Kind == runtime.TurnStarted {
				attempt = e.AttemptID
			}
		}
		if len(events) < 256 {
			break
		}
		if page == 999 {
			return nil, ErrAdmission
		}
	}
	if attempt == "" {
		return nil, ErrAdmission
	}
	return db.EvaluationHistory(ctx, task, attempt)
}

// ReviseFeedback requires the exact prior evaluation identity. It preserves all
// measurements and keeps the original subjective evidence in immutable history.
func ReviseFeedback(ctx context.Context, path, task, expectedID string, accepted bool) error {
	if expectedID == "" || len(expectedID) > 128 {
		return ErrAdmission
	}
	history, err := FeedbackHistory(ctx, path, task)
	if err != nil {
		return err
	}
	var prior evaluation.Record
	found := false
	for _, record := range history {
		if record.ID == expectedID {
			prior = record
			found = true
			break
		}
	}
	if !found {
		return ErrAdmission
	}
	next := prior
	hash := sha256.Sum256([]byte("feedback-revision:" + task + ":" + expectedID + ":" + strconv.FormatBool(accepted)))
	next.ID = hex.EncodeToString(hash[:])
	next.AllowJudge = false
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: next.ID, Passed: accepted}}
	if evaluation.ValidateRevision(prior, next) != nil {
		return ErrAdmission
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SupersedeEvaluation(ctx, expectedID, next)
}
