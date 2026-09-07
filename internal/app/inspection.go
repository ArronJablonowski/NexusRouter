package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrInspection = errors.New("task history unavailable or invalid")

// InspectTask is read-only. It neither initializes storage nor dispatches work.
// Return no partial conversation if the durable history cannot be validated.
func InspectTask(ctx context.Context, path, task string) (sessions.Snapshot, error) {
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) {
		return sessions.Snapshot{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failure := func() (sessions.Snapshot, error) {
		if ctx.Err() != nil {
			return sessions.Snapshot{}, ctx.Err()
		}
		return sessions.Snapshot{}, ErrInspection
	}
	if ctx.Err() != nil {
		return failure()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure()
	}
	defer db.Close()
	snapshot, err := db.TaskSnapshot(ctx, task)
	if err != nil {
		return failure()
	}
	return snapshot, nil
}

// InspectTaskContinuation reports only durable-history readiness. It does not
// choose a provider, grant execution authority, or promise admission of a future
// request. The result excludes conversation and tool payloads.
func InspectTaskContinuation(ctx context.Context, path, task string) (sessions.ContinuationStatus, error) {
	zero := sessions.ContinuationStatus{}
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failure := func() (sessions.ContinuationStatus, error) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	if ctx.Err() != nil {
		return failure()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return failure()
	}
	defer db.Close()
	status, err := db.TaskContinuation(ctx, task)
	if err != nil {
		return failure()
	}
	return status, nil
}

// InspectRouteExplanation returns the bounded metadata-only routing decision
// for an automatic task. Explicit tasks have no route-selection explanation.
func InspectRouteExplanation(ctx context.Context, path, task string) (sessions.RouteExplanation, error) {
	zero := sessions.RouteExplanation{}
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) {
		return zero, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	defer db.Close()
	out, err := db.RouteExplanation(ctx, task)
	if err != nil || out.Validate() != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrInspection
	}
	return out, nil
}
