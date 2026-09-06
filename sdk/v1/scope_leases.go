package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// ScopeLeaseStatus is metadata only, not permission to reclaim ownership.
type ScopeLeaseStatus = workers.ScopeLeaseStatus
type ScopeLeaseHolder = workers.ScopeLeaseHolder

// InspectScopeLeases reads overlapping holder metadata from existing storage.
// The requested scope is intentionally included; private holder scopes are not.
func (c *Client) InspectScopeLeases(ctx context.Context, scope string) (ScopeLeaseStatus, error) {
	if !c.valid(ctx) {
		return ScopeLeaseStatus{Version: 1}, ErrAdmission
	}
	status, err := app.InspectScopeLeases(ctx, c.database, scope)
	status.Version = 1
	return status, err
}
