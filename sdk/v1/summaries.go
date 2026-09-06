package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type SummaryAttempt = sessions.SummaryAttempt
type SummaryReview = sessions.SummaryReview

// SummarizeTask explicitly requests one bounded auxiliary draft. It never
// approves the proposal or applies it to a continuation automatically.
func (c *Client) SummarizeTask(ctx context.Context, task, modelID string, keep int, maxCost float64) (SummaryAttempt, error) {
	if !c.valid(ctx) {
		return SummaryAttempt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryAttempt{}, err
	}
	return c.service.SummarizeTask(ctx, task, modelID, keep, maxCost)
}

// InspectSummaryAttempt reads an existing proposal without creating storage,
// dispatching inference, or retaining a persistent database handle.
func (c *Client) InspectSummaryAttempt(ctx context.Context, id string) (SummaryAttempt, error) {
	if !c.valid(ctx) {
		return SummaryAttempt{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryAttempt{}, err
	}
	return app.InspectSummaryAttempt(ctx, c.database, id)
}

// ListSummaryAttempts reads one page from existing storage. The cursor is the
// prior attempt ID; inspection does not create storage or start background work.
func (c *Client) ListSummaryAttempts(ctx context.Context, task, after string, limit int) ([]SummaryAttempt, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return app.ListSummaryAttempts(ctx, c.database, task, after, limit)
}

// ReviewSummary records an explicit operator decision against the expected
// previous review ID. It is not an LLM judgment or automatic approval, and does
// not start a continuation. Hosts must authenticate the reviewing operator.
func (c *Client) ReviewSummary(ctx context.Context, attempt, expected, decision, note string) (SummaryReview, error) {
	if !c.valid(ctx) {
		return SummaryReview{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return SummaryReview{}, err
	}
	return c.service.ReviewSummary(ctx, attempt, expected, decision, note)
}

// SummaryReviewHistory reads recorded operator decisions without creating
// storage or retaining a persistent database handle.
func (c *Client) SummaryReviewHistory(ctx context.Context, attempt string) ([]SummaryReview, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return app.SummaryReviewHistory(ctx, c.database, attempt)
}
