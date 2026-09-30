package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type SessionTask = sessions.SessionTask
type SessionTaskPage = sessions.SessionTaskPage
type SessionTaskListOptions = sessions.SessionTaskListOptions

// ListSessionTasks returns durable, content-free task and lineage metadata for
// one session. Listing does not determine whether any task can be continued.
func (c *Client) ListSessionTasks(ctx context.Context, session string, options SessionTaskListOptions) (SessionTaskPage, error) {
	if !c.valid(ctx) || options.Validate(session) != nil {
		return SessionTaskPage{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SessionTaskPage{}, err
	}
	return c.service.ListSessionTasks(ctx, session, options)
}
