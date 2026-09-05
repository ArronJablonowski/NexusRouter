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
