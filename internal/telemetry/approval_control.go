package telemetry

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/approvals"
)

// OpenApprovalControl opens only an existing current-schema WAL database.
// It cannot create a missing database or migrate an older one.
func OpenApprovalControl(ctx context.Context, path string) (*Store, error) {
	return openExistingControl(ctx, path, approvals.ErrUnavailable)
}
