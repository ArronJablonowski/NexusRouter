package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type TaskHeadFence = sessions.TaskHeadFence

// SubmitBranch durably queues a new direct child of the exact completed source
// head. Retry uncertain delivery with the same key, source fence, and request.
func (c *Client) SubmitBranch(ctx context.Context, key string, source TaskHeadFence, request Request) (submissions.Status, error) {
	if !c.valid(ctx) || request.Version != 1 || source.Validate() != nil || request.ContinueTaskID != "" || request.Compaction != nil || request.SummaryAttemptID != "" {
		return submissions.Status{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Status{}, err
	}
	return c.service.SubmitBranch(ctx, key, source, request.internal())
}
