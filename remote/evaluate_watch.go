package remote

import (
	"context"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// WatchRecordedEvaluation waits for an explicitly selected, durably bound task
// to succeed, then invokes the existing one-attempt evaluator orchestration.
// It never dispatches, discovers, cancels tasks or retries uncertain evaluator
// calls. Transport/authentication failures stop the watcher for operator review.
// A host may supervise this bounded call in the background. Restart with the
// same request, evidence root and evaluator policy to retain replay protection.
func (c *Client) WatchRecordedEvaluation(ctx context.Context, routes *RouteStore, root, key string, task Task, policy RemoteEvaluator, wait, poll time.Duration) (RemoteEvaluationStatus, error) {
	status := RemoteEvaluationStatus{Version: 1, Status: "waiting"}
	if c == nil || ctx == nil || routes == nil || task.Validate() != nil || wait <= 0 || wait > 24*time.Hour || poll < time.Second || poll > time.Minute || !filepath.IsAbs(root) || filepath.Clean(root) != root || policy.Timeout <= 0 || policy.Timeout > evaluation.MaxReviewDuration {
		return RemoteEvaluationStatus{}, ErrInvalid
	}
	if task.Private && !policy.Local {
		return RemoteEvaluationStatus{}, ErrDenied
	}
	if _, err := evaluation.DescribeEvaluator(policy.Evaluator); err != nil {
		return RemoteEvaluationStatus{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return status, err
		}
		// Re-read both immutable local intent and fresh remote authorization each
		// poll. A changed/missing binding is never repaired by dispatching again.
		binding, err := routes.Lookup(key)
		if err != nil {
			return status, err
		}
		if binding.TaskSHA256 != hash(task) {
			return status, ErrConflict
		}
		var current submissions.Status
		err = c.callPinned(ctx, binding.Destination, "inspect", "GET", "/v1/remote/tasks/"+key, nil, nil, &current, binding.CallerFingerprint)
		if err != nil {
			return status, err
		}
		if current.Version != 1 || current.ID == "" {
			return status, ErrInvalid
		}
		switch current.State {
		case "succeeded":
			return c.EvaluateRecordedOutcome(ctx, routes, root, key, task, policy)
		case "failed", "canceled":
			status.Status = "task_" + current.State
			return status, nil
		case "queued", "running":
		default:
			return status, ErrInvalid
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}
