package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type TaskSummary = sessions.TaskSummary
type TaskPage = sessions.TaskPage
type TaskListOptions = sessions.TaskListOptions

// ListTasks returns newest-first durable metadata without conversation content.
// A listed task must still pass InspectTaskContinuation before continuation.
func (c *Client) ListTasks(ctx context.Context, options TaskListOptions) (TaskPage, error) {
	if !c.valid(ctx) {
		return TaskPage{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return TaskPage{}, err
	}
	return c.service.ListTasks(ctx, options)
}
