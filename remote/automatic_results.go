package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// ResolveAutomatic checks both durable records against the original saved
// request. It never repairs missing bindings, dispatches or performs discovery.
func (s *RouteStore) ResolveAutomatic(key string, request AutomaticRequest) (Task, RouteBinding, error) {
	if s == nil {
		return Task{}, RouteBinding{}, ErrInvalid
	}
	choice, err := s.AutomaticChoice(key)
	if err != nil {
		return Task{}, RouteBinding{}, err
	}
	task, err := choice.Task(request)
	if err != nil {
		return Task{}, RouteBinding{}, err
	}
	binding, err := s.Lookup(key)
	if err != nil {
		return Task{}, RouteBinding{}, err
	}
	if binding.Destination != choice.Destination || binding.CallerFingerprint != choice.CallerFingerprint || binding.TaskSHA256 != hash(task) {
		return Task{}, RouteBinding{}, ErrConflict
	}
	return task, binding, nil
}
func (c *Client) AutomaticStatus(ctx context.Context, routes *RouteStore, key string, request AutomaticRequest) (submissions.Status, error) {
	return c.automaticControl(ctx, routes, key, request, false)
}
func (c *Client) CancelAutomatic(ctx context.Context, routes *RouteStore, key string, request AutomaticRequest) (submissions.Status, error) {
	return c.automaticControl(ctx, routes, key, request, true)
}
func (c *Client) automaticControl(ctx context.Context, routes *RouteStore, key string, request AutomaticRequest, cancel bool) (submissions.Status, error) {
	var out submissions.Status
	if c == nil || ctx == nil || ctx.Err() != nil {
		return out, ErrInvalid
	}
	_, binding, err := routes.ResolveAutomatic(key, request)
	if err != nil {
		return out, err
	}
	op, method, path := "inspect", "GET", "/v1/remote/tasks/"+key
	if cancel {
		op, method, path = "cancel", "POST", path+"/cancel"
	}
	err = c.callPinned(ctx, binding.Destination, op, method, path, nil, nil, &out, binding.CallerFingerprint)
	return out, err
}
func (c *Client) AutomaticOutcome(ctx context.Context, routes *RouteStore, key string, request AutomaticRequest) (VerifiedOutcome, error) {
	task, _, err := routes.ResolveAutomatic(key, request)
	if err != nil {
		return VerifiedOutcome{}, err
	}
	return c.RecordedOutcome(ctx, routes, key, task)
}

// ReviewAutomaticOutcome applies the same operator/evaluator authentication and
// bound-review requirements as ReviewRecordedOutcome; it performs no judging.
func (c *Client) ReviewAutomaticOutcome(ctx context.Context, routes *RouteStore, root, key string, request AutomaticRequest, review OutcomeReview) error {
	task, _, err := routes.ResolveAutomatic(key, request)
	if err != nil {
		return err
	}
	return c.ReviewRecordedOutcome(ctx, routes, root, key, task, review)
}
