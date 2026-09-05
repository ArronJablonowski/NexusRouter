package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// InspectApproval returns validated metadata only, bound to the requested task.
// It never creates/migrates storage, grants authority, or retries a tool effect.
func InspectApproval(ctx context.Context, path, task, id string) (approvals.Record, error) {
	var result approvals.Record
	if ctx == nil || path == "" || !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) {
		return result, ErrAdmission
	}
	err := readApprovals(ctx, path, func(db *telemetry.Store, readCtx context.Context) error {
		var err error
		result, err = db.ReadApproval(readCtx, id)
		if err != nil {
			return err
		}
		if result.Validate() != nil || result.Request.TaskID != task || result.Request.ID != id {
			return ErrInspection
		}
		return nil
	})
	if err != nil {
		return approvals.Record{}, err
	}
	return result, nil
}

// ListApprovals reads one bounded keyset page. Independent pages are not a
// frozen snapshot; new calls which sort before the cursor require a new scan.
func ListApprovals(ctx context.Context, path string, options approvals.ListOptions) (approvals.Page, error) {
	var result approvals.Page
	if ctx == nil || path == "" || options.Validate() != nil {
		return result, ErrAdmission
	}
	err := readApprovals(ctx, path, func(db *telemetry.Store, readCtx context.Context) error {
		var err error
		result, err = db.ListApprovals(readCtx, options)
		if err != nil {
			return err
		}
		if result.Validate() != nil || result.Query != options {
			return ErrInspection
		}
		return nil
	})
	if err != nil {
		return approvals.Page{}, err
	}
	return result, nil
}

func readApprovals(ctx context.Context, path string, read func(*telemetry.Store, context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err == nil {
		defer db.Close()
		err = read(db, ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrInspection
	}
	return nil
}
