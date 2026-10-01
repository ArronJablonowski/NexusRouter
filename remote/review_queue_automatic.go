package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type queuedAutomaticReview struct {
	RequestID         string           `json:"request_id"`
	CallerFingerprint string           `json:"caller_fingerprint"`
	Request           AutomaticRequest `json:"request"`
}

func (j queuedReview) key() string {
	if j.Automatic != nil {
		return j.Automatic.RequestID
	}
	return j.Binding.RequestID
}
func (j queuedReview) private() bool {
	if j.Automatic != nil {
		return j.Automatic.Request.Routing.LocalRequired
	}
	return j.Task.Private
}
func validReviewRequest(request AutomaticRequest) bool {
	if request.Version != 1 || len(request.Prompt) == 0 || len(request.Prompt) > MaxBody/2 || request.Routing.AllowExploration || request.Routing.ContextTokens < 8192 || request.Routing.ContextTokens > 1<<24 {
		return false
	}
	_, err := harness.SelectScoped(request.Routing, harness.DefaultPolicy(), nil, time.Now().UTC(), 0)
	return err == nil || errors.Is(err, harness.ErrNoRoute)
}
func (j queuedReview) valid() bool {
	if j.Version != 1 || j.Deadline.IsZero() || j.Timeout <= 0 || j.Timeout > evaluation.MaxReviewDuration {
		return false
	}
	if j.Automatic != nil {
		a := j.Automatic
		return j.Binding == (RouteBinding{}) && hash(j.Task) == hash(Task{}) && requestID(a.RequestID) && hexDigest(a.CallerFingerprint) && validReviewRequest(a.Request)
	}
	return j.Binding.valid() && j.Task.Validate() == nil && j.Binding.TaskSHA256 == hash(j.Task) && j.Task.ExpectedHarnessIdentity != nil
}
func (j queuedReview) resolve(routes *RouteStore) (Task, error) {
	if j.Automatic != nil {
		task, choice, err := routes.ResolveAutomatic(j.key(), j.Automatic.Request)
		if err != nil {
			return Task{}, err
		}
		if choice.CallerFingerprint != j.Automatic.CallerFingerprint {
			return Task{}, ErrConflict
		}
		return task, nil
	}
	binding, err := routes.Lookup(j.key())
	if err != nil {
		return Task{}, err
	}
	if binding != j.Binding {
		return Task{}, ErrConflict
	}
	return j.Task, nil
}
func (c *Client) processReviewJob(ctx context.Context, routes *RouteStore, root string, policy func(bool) (RemoteEvaluator, error), job queuedReview, state ReviewJobStatus) ReviewJobStatus {
	state.Status = "attention"
	if routes.directory != job.Routes || root != job.Evidence {
		return state
	}
	if !time.Now().Before(job.Deadline) {
		state.Status = "expired"
		return state
	}
	configured, pe := policy(job.private())
	descriptor, de := evaluation.DescribeEvaluator(configured.Evaluator)
	if pe != nil || de != nil || hash(descriptor) != hash(job.Evaluator) || configured.Local != job.Local || configured.Timeout != job.Timeout {
		return state
	}
	task, err := job.resolve(routes)
	if err != nil {
		// A saved pre-dispatch intent alone is never permission to submit work.
		// Wait for its caller to publish the bound choice, or expire without quality.
		if job.Automatic != nil && errors.Is(err, os.ErrNotExist) {
			state.Status = "pending"
		}
		return state
	}
	current, err := c.InspectRecorded(ctx, routes, job.key())
	if err != nil {
		return state
	}
	switch current.Status.State {
	case "queued", "running":
		state.Status = "pending"
	case "failed", "canceled":
		state.Status = "task_" + current.Status.State
	case "succeeded":
		reviewCtx, cancel := context.WithTimeout(ctx, min(configured.Timeout, time.Until(job.Deadline)))
		defer cancel()
		evaluated, err := c.EvaluateRecordedOutcome(reviewCtx, routes, root, job.key(), task, configured)
		if err == nil && evaluated.Status == "completed" {
			state.Status = "completed"
			state.ReviewApplied = evaluated.ReviewApplied
		}
	}
	return state
}

// DispatchQueuedAutomaticReview durably enrolls original requirements BEFORE
// automatic selection/submission. Lost dispatch responses retain review intent.
// The queue lock fences its worker until dispatch returns or this process exits.
// The worker only resolves/inspects the saved choice; it can never dispatch it.
func (c *Client) DispatchQueuedAutomaticReview(ctx context.Context, q *ReviewQueue, routes *RouteStore, root, key string, request AutomaticRequest, reviewer RemoteEvaluator, deadline time.Time) (submissions.Status, AutomaticChoice, error) {
	var status submissions.Status
	var choice AutomaticChoice
	if ctx == nil || ctx.Err() != nil || c == nil || routes == nil || !requestID(key) || !validReviewRequest(request) || !deadline.After(time.Now()) || deadline.After(time.Now().Add(24*time.Hour)) || reviewer.Timeout <= 0 || reviewer.Timeout > evaluation.MaxReviewDuration {
		return status, choice, ErrInvalid
	}
	if request.Routing.LocalRequired && !reviewer.Local {
		return status, choice, ErrDenied
	}
	descriptor, err := evaluation.DescribeEvaluator(reviewer.Evaluator)
	if err != nil {
		return status, choice, ErrInvalid
	}
	if err = q.check(); err != nil {
		return status, choice, err
	}
	if err = routes.check(); err != nil {
		return status, choice, err
	}
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 {
		return status, choice, ErrDenied
	}
	release, err := lockReviewQueue(q.directory)
	if err != nil {
		return status, choice, err
	}
	defer release()
	if err = PrepareOutcomeEvidence(root); err != nil {
		return status, choice, err
	}
	job := queuedReview{Version: 1, Automatic: &queuedAutomaticReview{RequestID: key, CallerFingerprint: certificateDigest(cert.Certificate[0]), Request: request}, Routes: routes.directory, Evidence: root, Evaluator: descriptor, Local: reviewer.Local, Timeout: reviewer.Timeout, Deadline: deadline.UTC()}
	body, err := json.Marshal(job)
	if err != nil {
		return status, choice, err
	}
	if err = publishPrivateDocument(q.path(key, ".job.json"), body, MaxBody*2); err != nil {
		return status, choice, err
	}
	// Do not submit again after terminal supervision, even if the remote route
	// was never established. Inspection remains available independently.
	saved, err := q.status(job)
	if err != nil {
		return status, choice, err
	}
	if saved.Status != "pending" {
		return status, choice, ErrConflict
	}
	return c.DispatchDiscovered(ctx, routes, root, key, request, harness.DefaultPolicy(), 0)
}
