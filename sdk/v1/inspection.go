package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrInspection = app.ErrInspection

// ContinuationStatus is metadata-only history readiness, not provider admission
// or permission to retry interrupted effects.
type ContinuationStatus = sessions.ContinuationStatus

// RouteExplanation is a metadata-only automatic routing decision. It contains
// no prompt, output, endpoint, credential, or tool payload fields.
type RouteExplanation = sessions.RouteExplanation

func (c *Client) InspectTaskContinuation(ctx context.Context, task string) (ContinuationStatus, error) {
	if !c.valid(ctx) {
		return ContinuationStatus{Version: 1}, ErrAdmission
	}
	status, err := app.InspectTaskContinuation(ctx, c.database, task)
	status.Version = 1
	return status, err
}

// TaskSnapshot is a versioned, read-only view of durable task state. Its message
// and tool payloads may be sensitive. Interrupted or uncertain state never
// implies that repeating inference or effects is safe.
type TaskSnapshot struct {
	Version int
	sessions.Snapshot
}

// InspectTask reads a single bounded database snapshot without creating storage,
// migrating schemas, or resuming execution. Failure never returns partial text.
func (c *Client) InspectTask(ctx context.Context, task string) (TaskSnapshot, error) {
	if !c.valid(ctx) {
		return TaskSnapshot{Version: 1}, ErrAdmission
	}
	snapshot, err := app.InspectTask(ctx, c.database, task)
	return TaskSnapshot{Version: 1, Snapshot: snapshot}, err
}

func (c *Client) InspectRouteExplanation(ctx context.Context, task string) (RouteExplanation, error) {
	if !c.valid(ctx) {
		return RouteExplanation{Version: 1}, ErrAdmission
	}
	out, err := app.InspectRouteExplanation(ctx, c.database, task)
	out.Version = 1
	return out, err
}
