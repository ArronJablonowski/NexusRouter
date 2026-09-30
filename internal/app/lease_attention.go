package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

// InspectLeaseAttention reads durable operator-attention metadata without
// migrations, model calls, ownership probes, or recovery mutations.
func InspectLeaseAttention(ctx context.Context, path string, options workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
	zero := workers.LeaseAttentionPage{}
	if ctx == nil || path == "" || options.Validate() != nil {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	failure := func() (workers.LeaseAttentionPage, error) {
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
	page, err := db.ListLeaseAttention(ctx, options)
	if err != nil {
		return failure()
	}
	return page, nil
}
