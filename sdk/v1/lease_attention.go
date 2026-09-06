package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

type LeaseAttention = workers.LeaseAttention
type LeaseAttentionPage = workers.LeaseAttentionPage
type LeaseAttentionOptions = workers.LeaseAttentionOptions

// ListLeaseAttention lists durable attention records. These observations grant
// no authority to release leases, retry effects, or accept recovered output.
func (c *Client) ListLeaseAttention(ctx context.Context, options LeaseAttentionOptions) (LeaseAttentionPage, error) {
	if !c.valid(ctx) {
		return LeaseAttentionPage{Version: 1}, ErrAdmission
	}
	page, err := app.InspectLeaseAttention(ctx, c.database, options)
	page.Version = 1
	return page, err
}
