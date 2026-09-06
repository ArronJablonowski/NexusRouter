package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// InspectTaskLeases returns metadata only. Expiry and recovery counts are not
// authority to release a lease, retry an effect, or accept worker output.
func InspectTaskLeases(ctx context.Context, path, task string) (workers.TaskLeaseStatus, error) {
	zero := workers.TaskLeaseStatus{}
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	failure := func() (workers.TaskLeaseStatus, error) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	if ctx.Err() != nil {
		return failure()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure()
	}
	defer db.Close()
	status, err := db.TaskLeaseStatus(ctx, task, time.Now().UTC())
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, sql.ErrNoRows) {
			return zero, sql.ErrNoRows
		}
		return failure()
	}
	return status, nil
}
