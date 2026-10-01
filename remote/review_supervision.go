package remote

import (
	"context"
	"errors"
	"time"
)

// ErrReviewQueueBusy means another local worker or dispatch currently owns the
// queue. It is retryable; storage, integrity and policy failures are not.
var ErrReviewQueueBusy = errors.New("remote review queue busy")

// RunReviewJobs supervises an existing queue until cancellation or a queue-level
// failure. It checks immediately, then every fifteen seconds after each pass.
// It never dispatches work. Individual terminal job receipts are retained by
// ProcessReviewJobs. The caller must cancel and join this call before closing
// its evaluator, resource coordinator or evidence databases.
func (c *Client) RunReviewJobs(ctx context.Context, q *ReviewQueue, routes *RouteStore, root string, policy func(bool) (RemoteEvaluator, error)) error {
	return c.runReviewJobs(ctx, q, routes, root, policy, 15*time.Second)
}

func (c *Client) runReviewJobs(ctx context.Context, q *ReviewQueue, routes *RouteStore, root string, policy func(bool) (RemoteEvaluator, error), interval time.Duration) error {
	if ctx == nil || c == nil || routes == nil || policy == nil || interval <= 0 {
		return ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := c.ProcessReviewJobs(ctx, q, routes, root, policy)
		if err != nil && !errors.Is(err, ErrReviewQueueBusy) {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
