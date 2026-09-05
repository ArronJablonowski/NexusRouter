package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// TaskContinuation derives snapshot and checkpoint from the same bounded,
// replay-validated read transaction. It neither repairs a task nor appends a
// continuation, and returns no partially decoded status on any failure.
func (s *Store) TaskContinuation(ctx context.Context, task string) (sessions.ContinuationStatus, error) {
	zero := sessions.ContinuationStatus{}
	if ctx == nil || !sessions.ValidEventPageID(task) {
		return zero, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil {
		return zero, err
	}
	tail := events
	if len(tail) > 2 {
		tail = tail[len(tail)-2:]
	}
	out := sessions.AssessContinuation(snapshot, tail)
	if out.Validate() != nil {
		return zero, sessions.ErrHistory
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}
