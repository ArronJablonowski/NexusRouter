package v1

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

// InspectApproval reads task-bound approval metadata, without invoking a
// reviewer or authorizing/retrying work. Consumed is not proof of an effect.
func (c *Client) InspectApproval(ctx context.Context, task, id string) (approvals.Record, error) {
	if !c.valid(ctx) {
		return approvals.Record{}, ErrAdmission
	}
	return app.InspectApproval(ctx, c.database, task, id)
}

// ListApprovals returns a bounded read-only page ordered by tool-call ID.
// A continuation is not a frozen snapshot. Start a new scan to see newly added
// IDs that sort before the previous cursor. Raw arguments are never included.
func (c *Client) ListApprovals(ctx context.Context, options approvals.ListOptions) (approvals.Page, error) {
	if !c.valid(ctx) {
		return approvals.Page{}, ErrAdmission
	}
	return app.ListApprovals(ctx, c.database, options)
}
