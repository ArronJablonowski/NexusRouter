package app

import (
	"context"
	"errors"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// ListTasks discovers content-free durable task metadata. It neither creates
// storage nor determines continuation eligibility for any listed task.
func (s *Service) ListTasks(ctx context.Context, options sessions.TaskListOptions) (sessions.TaskPage, error) {
	zero := sessions.TaskPage{}
	if ctx == nil || options.Validate() != nil {
		return zero, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(options, secrets) {
		return zero, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) && options.After == "" {
			page := sessions.TaskPage{Version: 1, Items: []sessions.TaskSummary{}}
			if ctx.Err() != nil || !selectionValueClean(page, append(secrets, memorySecrets(s.settings, s.secret)...)) {
				return zero, ErrInspection
			}
			return page, nil
		}
		return zero, ErrInspection
	}
	defer db.Close()
	page, err := db.ListTasks(ctx, options)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || page.Validate() != nil || len(page.Items) > options.Limit || !selectionValueClean(page, secrets) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	return page, nil
}
