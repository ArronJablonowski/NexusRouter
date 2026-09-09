package app

import (
	"context"
	"errors"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ListSessionTasks discovers content-free durable task and lineage metadata for
// one session. It neither creates storage nor determines continuation
// eligibility for any listed task.
func (s *Service) ListSessionTasks(ctx context.Context, session string, options sessions.SessionTaskListOptions) (sessions.SessionTaskPage, error) {
	zero := sessions.SessionTaskPage{}
	if ctx == nil || options.Validate(session) != nil {
		return zero, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]any{session, options}, secrets) {
		return zero, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) && options.After == "" {
			page := sessions.SessionTaskPage{Version: 1, SessionID: session, Items: []sessions.SessionTask{}}
			if ctx.Err() != nil || !selectionValueClean(page, append(secrets, memorySecrets(s.settings, s.secret)...)) {
				return zero, ErrInspection
			}
			return page, nil
		}
		return zero, ErrInspection
	}
	defer db.Close()
	page, err := db.ListSessionTasks(ctx, session, options)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || page.Validate() != nil || page.SessionID != session || len(page.Items) > options.Limit || !selectionValueClean(page, secrets) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	return page, nil
}
