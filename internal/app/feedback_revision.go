package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
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
	return FeedbackHistoryStore(ctx, db, task)
}

// FeedbackHistoryStore reuses an already-validated daemon store rather than
// revalidating the entire database on every feedback inspection.
func FeedbackHistoryStore(ctx context.Context, db *telemetry.Store, task string) ([]evaluation.Record, error) {
	if task == "" || len(task) > 128 {
		return nil, ErrAdmission
	}
	if db == nil {
		return nil, errors.New("feedback database unavailable")
	}
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
	next, err := feedbackRevision(history, task, expectedID, accepted)
	if err != nil {
		return err
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SupersedeEvaluation(ctx, expectedID, next)
}

// ReviseFeedbackStore keeps daemon revisions on the same store as execution
// and initial feedback; a second writable open can delay dispatcher leases.
func ReviseFeedbackStore(ctx context.Context, db *telemetry.Store, task, expectedID string, accepted bool) error {
	if expectedID == "" || len(expectedID) > 128 {
		return ErrAdmission
	}
	history, err := FeedbackHistoryStore(ctx, db, task)
	if err != nil {
		return err
	}
	next, err := feedbackRevision(history, task, expectedID, accepted)
	if err != nil {
		return err
	}
	return db.SupersedeEvaluation(ctx, expectedID, next)
}

// WithdrawFeedback removes invalid subjective evidence from quality learning
// without deleting its original judgment or changing execution measurements.
func WithdrawFeedback(ctx context.Context, path, task, expectedID string) error {
	if expectedID == "" || len(expectedID) > 128 {
		return ErrAdmission
	}
	history, err := FeedbackHistory(ctx, path, task)
	if err != nil {
		return err
	}
	next, err := feedbackWithdrawal(history, task, expectedID)
	if err != nil {
		return err
	}
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SupersedeEvaluation(ctx, expectedID, next)
}

func WithdrawFeedbackStore(ctx context.Context, db *telemetry.Store, task, expectedID string) error {
	if expectedID == "" || len(expectedID) > 128 {
		return ErrAdmission
	}
	history, err := FeedbackHistoryStore(ctx, db, task)
	if err != nil {
		return err
	}
	next, err := feedbackWithdrawal(history, task, expectedID)
	if err != nil {
		return err
	}
	return db.SupersedeEvaluation(ctx, expectedID, next)
}

func feedbackWithdrawal(history []evaluation.Record, task, expectedID string) (evaluation.Record, error) {
	for _, prior := range history {
		if prior.ID != expectedID {
			continue
		}
		next := prior
		hash := sha256.Sum256([]byte("feedback-withdrawal:" + task + ":" + expectedID))
		next.ID = hex.EncodeToString(hash[:])
		next.AllowJudge = false
		next.Checks = []evaluation.Check{{Source: evaluation.Withdrawn, Reference: next.ID}}
		if evaluation.ValidateRevision(prior, next) != nil {
			return evaluation.Record{}, ErrAdmission
		}
		return next, nil
	}
	return evaluation.Record{}, ErrAdmission
}

func feedbackRevision(history []evaluation.Record, task, expectedID string, accepted bool) (evaluation.Record, error) {
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
		return evaluation.Record{}, ErrAdmission
	}
	next := prior
	hash := sha256.Sum256([]byte("feedback-revision:" + task + ":" + expectedID + ":" + strconv.FormatBool(accepted)))
	next.ID = hex.EncodeToString(hash[:])
	next.AllowJudge = false
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: next.ID, Passed: accepted}}
	if evaluation.ValidateRevision(prior, next) != nil {
		return evaluation.Record{}, ErrAdmission
	}
	return next, nil
}
