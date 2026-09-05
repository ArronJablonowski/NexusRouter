package app

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"darwinrouter/internal/telemetry"
	"darwinrouter/sessions"
	"darwinrouter/submissions"
)

// SubmissionRecoveries reads the immutable supervisor audit trail. It never
// grants an operator/model authority to replay uncertain work.
func (s *Service) SubmissionRecoveries(ctx context.Context, id string) ([]submissions.Recovery, error) {
	if !sessions.ValidEventPageID(id) {
		return nil, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sql.ErrNoRows
		}
		return nil, ErrSubmission
	}
	defer db.Close()
	history, err := db.RecoveryHistory(ctx, id)
	return history, submissionError(err)
}
