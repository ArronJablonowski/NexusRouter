package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// ReadEvents returns committed events strictly after the supplied sequence.
// Use zero for the first page and persist NextSequence only after processing
// the page successfully. This is inspection, not a live subscription or retry.
func (c *Client) ReadEvents(ctx context.Context, task string, after int64, limit int) (sessions.EventPage, error) {
	if !c.valid(ctx) {
		return sessions.EventPage{}, ErrAdmission
	}
	return app.ReadTaskEvents(ctx, c.database, task, after, limit)
}
