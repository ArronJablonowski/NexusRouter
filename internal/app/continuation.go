package app

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type continuationContext struct {
	Messages           []providers.Message
	SessionID, Privacy string
	Compaction         *runtime.ContextCompaction
}

func loadContinuation(ctx context.Context, db sessions.Reader, r Request, secrets []string) (*continuationContext, error) {
	history, err := sessions.Replay(ctx, db, r.ContinueTaskID)
	if err != nil || history.State != "completed" || history.InterruptedTurn || history.UncertainEffects || len(history.Pending) > 0 {
		return nil, ErrAdmission
	}
	result := &continuationContext{Messages: history.Messages, SessionID: history.SessionID, Privacy: history.Privacy}
	if r.Compaction != nil {
		// Redact before both provider assembly and checkpoint persistence.
		request := *r.Compaction
		request.Summary = redactSummary(request.Summary, secrets)
		result.Messages, result.Compaction, err = sessions.PrepareContinuation(history, request)
		if err != nil {
			return nil, ErrAdmission
		}
	}
	if r.SummaryAttemptID != "" {
		store, ok := db.(interface {
			SummaryAttempt(context.Context, string) (sessions.SummaryAttempt, error)
			CurrentSummaryReview(context.Context, string) (sessions.SummaryReview, error)
		})
		if !ok {
			return nil, ErrAdmission
		}
		attempt, err := store.SummaryAttempt(ctx, r.SummaryAttemptID)
		if err != nil || attempt.Status != "drafted" || attempt.Draft == nil || attempt.TaskID != history.TaskID {
			return nil, ErrAdmission
		}
		review, err := store.CurrentSummaryReview(ctx, attempt.ID)
		if err != nil || review.Validate() != nil || review.AttemptID != attempt.ID || review.Decision != "approved" {
			return nil, ErrAdmission
		}
		request := attempt.Draft.Request
		request.Summary = redactSummary(request.Summary, secrets)
		result.Messages, result.Compaction, err = sessions.PrepareContinuation(history, request)
		if err != nil {
			return nil, ErrAdmission
		}
		// The operator reviewed this exact immutable proposal. If newly
		// configured redaction changes it, require a fresh draft and review.
		expected, err := json.Marshal(attempt.Draft.Checkpoint)
		actual, encodeErr := json.Marshal(result.Compaction)
		if err != nil || encodeErr != nil || !bytes.Equal(expected, actual) {
			return nil, ErrAdmission
		}
		result.Compaction.SummaryAttemptID, result.Compaction.SummaryReviewID = attempt.ID, review.ID
	}
	return result, nil
}

func redactSummary(summary sessions.Summary, secrets []string) sessions.Summary {
	fields := []*[]string{&summary.Decisions, &summary.PendingWork, &summary.Failures, &summary.Artifacts, &summary.Requirements, &summary.Activity}
	for _, field := range fields {
		copy := append([]string(nil), (*field)...)
		for i := range copy {
			copy[i] = redact(copy[i], secrets)
		}
		*field = copy
	}
	return summary
}
