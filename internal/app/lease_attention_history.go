package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

// InspectLeaseAttentionHistory reads immutable observation metadata without
// migration, ownership probes, recovery, or provider execution.
func InspectLeaseAttentionHistory(ctx context.Context, path, id string, options workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
	zero := workers.LeaseAttentionHistoryPage{}
	if ctx == nil || path == "" || !sessions.ValidEventPageID(id) || options.Validate() != nil {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	failure := func() (workers.LeaseAttentionHistoryPage, error) {
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
	page, err := db.ListLeaseAttentionHistory(ctx, id, options)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, sql.ErrNoRows) {
			return zero, sql.ErrNoRows
		}
		return failure()
	}
	return page, nil
}
