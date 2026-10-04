package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"time"
)

func (s *Service) ListJobTasks(ctx context.Context, options sessions.TaskListOptions) (sessions.TaskPage, error) {
	if ctx == nil || options.Validate() != nil || !selectionValueClean(options, memorySecrets(s.settings, s.secret)) {
		return sessions.TaskPage{}, ErrAdmission
	}
	db, closeDB, err := s.openTaskReadStore(ctx)
	if err != nil {
		return sessions.TaskPage{}, ErrInspection
	}
	defer closeDB()
	page, err := db.ListTasks(ctx, options)
	if err != nil {
		return sessions.TaskPage{}, ErrInspection
	}
	if err = db.ObserveTaskExecution(ctx, &page, time.Now()); err != nil || !selectionValueClean(page, memorySecrets(s.settings, s.secret)) {
		return sessions.TaskPage{}, ErrInspection
	}
	applyJobDismissals(&page, s.settings.Telemetry.Database)
	return page, nil
}
