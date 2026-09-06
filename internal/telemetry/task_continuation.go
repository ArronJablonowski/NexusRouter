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
	_, status, err := s.ContinuationSnapshot(ctx, task)
	return status, err
}

// ContinuationSnapshot shares the same coherent, raw-size-preflighted history
// between execution admission and metadata inspection. It grants no execution
// authority and does not rewrite interruption flags in the returned snapshot.
func (s *Store) ContinuationSnapshot(ctx context.Context, task string) (sessions.Snapshot, sessions.ContinuationStatus, error) {
	noSnapshot := sessions.Snapshot{}
	zero := sessions.ContinuationStatus{}
	if ctx == nil || !sessions.ValidEventPageID(task) {
		return noSnapshot, zero, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return noSnapshot, zero, err
	}
	defer tx.Rollback()
	var events []runtime.Event
	_, err = taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil {
		return noSnapshot, zero, err
	}
	snapshot, out, err := sessions.ReplayContinuation(ctx, snapshotEvents(events), task)
	if err != nil {
		return noSnapshot, zero, err
	}
	if out.Validate() != nil {
		return noSnapshot, zero, sessions.ErrHistory
	}
	if err := tx.Commit(); err != nil {
		return noSnapshot, zero, err
	}
	return snapshot, out, nil
}
