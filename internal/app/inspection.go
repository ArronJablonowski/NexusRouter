package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrInspection = errors.New("task history unavailable or invalid")

// InspectTask is read-only. It neither initializes storage nor dispatches work.
// Return no partial conversation if the durable history cannot be validated.
func InspectTask(ctx context.Context, path, task string) (sessions.Snapshot, error) {
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) {
		return sessions.Snapshot{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failure := func() (sessions.Snapshot, error) {
		if ctx.Err() != nil {
			return sessions.Snapshot{}, ctx.Err()
		}
		return sessions.Snapshot{}, ErrInspection
	}
	if ctx.Err() != nil {
		return failure()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure()
	}
	defer db.Close()
	snapshot, err := db.TaskSnapshot(ctx, task)
	if err != nil {
		return failure()
	}
	return snapshot, nil
}
