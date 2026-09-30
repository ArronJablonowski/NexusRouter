package telemetry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func cancellationStatus(ctx context.Context, tx *sql.Tx, task string, legacy bool) (runtime.CancellationStatus, error) {
	status := runtime.CancellationStatus{Version: 1, TaskID: task}
	if err := tx.QueryRowContext(ctx, "SELECT state FROM task_heads WHERE task_id=?", task).Scan(&status.State); err != nil {
		return status, err
	}
	if status.State != "running" && status.State != "completed" && status.State != "failed" && status.State != "canceled" {
		return status, sessions.ErrHistory
	}
	if legacy {
		return status, nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT request_id,requested_at FROM task_cancellations WHERE task_id=?", task).Scan(&status.RequestID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || at.IsZero() || !sessions.ValidEventPageID(status.RequestID) {
		return status, sessions.ErrHistory
	}
	status.Requested = true
	status.RequestedAt = &at
	return status, nil
}

func (s *Store) RequestCancellation(ctx context.Context, task string) (runtime.CancellationStatus, error) {
	return s.RequestCancellationAtRevision(ctx, task, 0)
}

// RequestCancellationAtRevision binds a new cancellation to an observed task
// head. An exact already-recorded cancellation is returned before the stale-head
// check so acknowledgement-loss retries remain safe after the runtime advances.
func (s *Store) RequestCancellationAtRevision(ctx context.Context, task string, expected int64) (runtime.CancellationStatus, error) {
	if !sessions.ValidEventPageID(task) {
		return runtime.CancellationStatus{}, sessions.ErrHistory
	}
	if expected < 0 || expected > sessions.MaxTaskEvents {
		return runtime.CancellationStatus{}, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtime.CancellationStatus{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", task); err != nil {
		return runtime.CancellationStatus{}, err
	}
	status, err := cancellationStatus(ctx, tx, task, false)
	if err != nil {
		return status, err
	}
	if status.Requested {
		if err := tx.Commit(); err != nil {
			return runtime.CancellationStatus{}, err
		}
		return status, nil
	}
	if expected > 0 {
		var current int64
		if err = tx.QueryRowContext(ctx, "SELECT sequence FROM task_heads WHERE task_id=?", task).Scan(&current); err != nil {
			return runtime.CancellationStatus{}, err
		}
		if current != expected {
			return runtime.CancellationStatus{}, ErrConflict
		}
	}
	if !status.Requested && status.State == "running" {
		at := time.Now().UTC()
		status.Requested = true
		status.RequestID = rand.Text()
		status.RequestedAt = &at
		if _, err := tx.ExecContext(ctx, "INSERT INTO task_cancellations(task_id,request_id,requested_at) VALUES(?,?,?)", task, status.RequestID, at.Format(time.RFC3339Nano)); err != nil {
			return runtime.CancellationStatus{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return runtime.CancellationStatus{}, err
	}
	return status, nil
}

func (s *Store) CancellationStatus(ctx context.Context, task string) (runtime.CancellationStatus, error) {
	if !sessions.ValidEventPageID(task) {
		return runtime.CancellationStatus{}, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtime.CancellationStatus{}, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return runtime.CancellationStatus{}, err
	}
	status, err := cancellationStatus(ctx, tx, task, version < 11)
	if err != nil {
		return status, err
	}
	if err := tx.Commit(); err != nil {
		return runtime.CancellationStatus{}, err
	}
	return status, nil
}

func (s *Store) CancellationRequested(ctx context.Context, task string) (bool, error) {
	if !sessions.ValidEventPageID(task) {
		return false, sessions.ErrHistory
	}
	var requested bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_cancellations WHERE task_id=?)", task).Scan(&requested)
	return requested, err
}
