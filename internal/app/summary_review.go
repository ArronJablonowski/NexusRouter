package app

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ReviewSummary records explicit operator review, not an LLM accuracy claim.
// Previous review identity is a compare-and-swap guard against stale decisions.
func (s *Service) ReviewSummary(ctx context.Context, attemptID, expectedID, decision, note string) (sessions.SummaryReview, error) {
	if len(note) > 4096 {
		return sessions.SummaryReview{}, ErrAdmission
	}
	r := sessions.SummaryReview{Version: 1, ID: rand.Text(), AttemptID: attemptID, PreviousID: expectedID, Decision: decision, Note: redact(note, memorySecrets(s.settings, s.secret)), Time: time.Now().UTC()}
	if r.Validate() != nil {
		return sessions.SummaryReview{}, ErrAdmission
	}
	read, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return sessions.SummaryReview{}, ErrAdmission
	}
	a, err := read.SummaryAttempt(ctx, attemptID)
	read.Close()
	if err != nil || a.Status != "drafted" {
		return sessions.SummaryReview{}, ErrAdmission
	}
	write, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return sessions.SummaryReview{}, ErrAdmission
	}
	defer write.Close()
	if err := write.RecordSummaryReview(ctx, r); err != nil {
		return sessions.SummaryReview{}, err
	}
	return r, nil
}

func SummaryReviewHistory(ctx context.Context, path, attemptID string) ([]sessions.SummaryReview, error) {
	if ctx == nil || path == "" || !summaryInspectionID(attemptID, false) {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, summaryInspectionError(ctx)
	}
	defer db.Close()
	result, err := db.SummaryReviews(ctx, attemptID)
	if err != nil || ctx.Err() != nil {
		return nil, summaryInspectionError(ctx)
	}
	if result == nil {
		result = []sessions.SummaryReview{}
	}
	return result, nil
}
