package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

type LeaseAttentionTransition = workers.LeaseAttentionTransition
type LeaseAttentionHistoryOptions = workers.LeaseAttentionHistoryOptions
type LeaseAttentionHistoryPage = workers.LeaseAttentionHistoryPage

// ListLeaseAttentionHistory reads observation transitions, not execution or
// recovery authority. The database must already exist.
func (c *Client) ListLeaseAttentionHistory(ctx context.Context, id string, options LeaseAttentionHistoryOptions) (LeaseAttentionHistoryPage, error) {
	if !c.valid(ctx) {
		return LeaseAttentionHistoryPage{Version: 1}, ErrAdmission
	}
	page, err := app.InspectLeaseAttentionHistory(ctx, c.database, id, options)
	page.Version = 1
	return page, err
}
