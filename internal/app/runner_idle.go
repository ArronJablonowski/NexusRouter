package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"time"
)

// RunnerIdle refuses lifecycle changes when any work or reservation is active.
func (s *Service) RunnerIdle(ctx context.Context) error {
	for _, state := range []string{"queued", "running"} {
		page, err := s.ListSubmissions(ctx, submissions.ListOptions{State: state, Limit: 1})
		if err != nil || len(page.Items) > 0 || page.HasMore {
			return ErrAdmission
		}
	}
	db, release, err := s.openTaskReadStore(ctx)
	if err != nil {
		return ErrAdmission
	}
	defer release()
	page, err := db.ListTasks(ctx, sessions.TaskListOptions{State: "running", Limit: 1})
	if err != nil || len(page.Items) > 0 || page.HasMore {
		return ErrAdmission
	}
	snapshot, installed, err := s.hostReservationSnapshot(ctx, time.Now())
	if err != nil || !installed || snapshot.Active != 0 {
		return ErrAdmission
	}
	return nil
}
