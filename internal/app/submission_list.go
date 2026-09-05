package app

import (
	"context"
	"errors"
	"os"

	"darwinrouter/internal/telemetry"
	"darwinrouter/submissions"
)

// ListSubmissions discovers durable work without loading private request or
// result contents and without creating or migrating an absent database.
func (s *Service) ListSubmissions(ctx context.Context, options submissions.ListOptions) (submissions.Page, error) {
	if err := options.Validate(); err != nil {
		return submissions.Page{}, submissions.ErrInvalid
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && options.After == "" {
			return submissions.Page{Version: 1, Items: []submissions.Summary{}}, nil
		}
		return submissions.Page{}, ErrSubmission
	}
	defer db.Close()
	page, err := db.ListSubmissions(ctx, options)
	return page, submissionError(err)
}
