package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ReadTaskEvents reads one committed page without creating or migrating storage.
// Error responses never expose a partially validated page.
func ReadTaskEvents(ctx context.Context, path, task string, after int64, limit int) (sessions.EventPage, error) {
	zero := sessions.EventPage{}
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) || limit < 1 || limit > 100 {
		return zero, ErrAdmission
	}
	if after < 0 {
		return zero, sessions.ErrEventCursor
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failure := func(err error) (sessions.EventPage, error) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		for _, known := range []error{sessions.ErrEventCursor, sessions.ErrEventTooLarge} {
			if errors.Is(err, known) {
				return zero, known
			}
		}
		return zero, ErrInspection
	}
	if ctx.Err() != nil {
		return failure(ctx.Err())
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure(err)
	}
	defer db.Close()
	page, err := db.ReadEventPage(ctx, task, after, limit)
	if err != nil {
		return failure(err)
	}
	return page, nil
}
