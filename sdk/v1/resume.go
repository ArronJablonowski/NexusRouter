package v1

import (
	"context"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

// SubmitResume durably queues new work from an exact recovered-history source.
// Retry uncertain delivery with the same key, source fence, and request.
func (c *Client) SubmitResume(ctx context.Context, key string, source TaskHeadFence, request Request) (submissions.Status, error) {
	if !c.valid(ctx) || request.Version != 1 || source.Validate() != nil || strings.TrimSpace(request.Prompt) == "" || len(request.Messages) != 0 || request.ContinueTaskID != "" || request.Compaction != nil || request.SummaryAttemptID != "" {
		return submissions.Status{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Status{}, err
	}
	return c.service.SubmitResume(ctx, key, source, request.internal())
}
