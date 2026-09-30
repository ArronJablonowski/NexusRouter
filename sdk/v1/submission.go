package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

var ErrSubmission = app.ErrSubmission

// Submit durably queues work only. A separately running daemon with matching
// configuration must execute it. Retry uncertain delivery with the same key and
// request, never a newly generated key. Keys contain 16–128 printable ASCII bytes.
func (c *Client) Submit(ctx context.Context, key string, request Request) (submissions.Status, error) {
	if !c.valid(ctx) || request.Version != 1 {
		return submissions.Status{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Status{}, err
	}
	return c.service.Submit(ctx, key, request.internal())
}

// SubmissionStatus reads durable work, including sensitive completed output.
// It does not create storage, migrate it, or start an execution worker.
func (c *Client) SubmissionStatus(ctx context.Context, id string) (submissions.Status, error) {
	if !c.valid(ctx) {
		return submissions.Status{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Status{}, err
	}
	return c.service.SubmissionStatus(ctx, id)
}

// CancelSubmission requests durable cancellation; running tools still require
// cooperative cleanup. A returned request is not proof all effects have stopped.
func (c *Client) CancelSubmission(ctx context.Context, id string) (submissions.Status, error) {
	if !c.valid(ctx) {
		return submissions.Status{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Status{}, err
	}
	return c.service.CancelSubmission(ctx, id)
}

// ListSubmissions returns bounded metadata without private prompts/results.
// Pagination cursors are opaque and tied to the original listing boundary.
func (c *Client) ListSubmissions(ctx context.Context, options submissions.ListOptions) (submissions.Page, error) {
	if !c.valid(ctx) {
		return submissions.Page{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return submissions.Page{}, err
	}
	return c.service.ListSubmissions(ctx, options)
}
