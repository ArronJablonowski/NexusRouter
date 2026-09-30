package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

// InspectScopeLeases observes overlapping holders without probing ownership,
// modifying storage, profiling hardware, or granting recovery authority.
func InspectScopeLeases(ctx context.Context, path, scope string) (workers.ScopeLeaseStatus, error) {
	zero := workers.ScopeLeaseStatus{}
	if ctx == nil || path == "" || !workers.ValidLeaseScope(scope) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	failure := func() (workers.ScopeLeaseStatus, error) {
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
	status, err := db.ScopeLeaseStatus(ctx, scope, time.Now().UTC())
	if err != nil {
		return failure()
	}
	return status, nil
}
