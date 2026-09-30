package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// FinishWorker atomically commits a terminal event and releases its reader
// lease. The trusted caller must have joined execution and validation first.
// An expired lease permits cancellation only, as with AppendWorker.
// This capability requires one historical lease per task/owner, including
// released leases. Ambiguous ownership is refused rather than inferred; no
// separate finalization receipt is persisted.
func (s *Store) FinishWorker(ctx context.Context, expected int64, e runtime.Event, leaseToken, owner, submissionID, submissionToken string) error {
	if leaseToken == "" || owner == "" || e.WorkerID != owner || (e.Kind != runtime.TaskCompleted && e.Kind != runtime.TaskFailed && e.Kind != runtime.TaskCanceled) {
		return runtime.ErrExecutionLeaseLost
	}
	return s.appendFencedFinal(ctx, expected, e, submissionID, submissionToken, leaseToken, owner, true, nil)
}

func (s *Store) FinishLeased(ctx context.Context, expected int64, e runtime.Event, token, owner string) error {
	return s.FinishWorker(ctx, expected, e, token, owner, "", "")
}

func finishedWorkerLease(ctx context.Context, tx *sql.Tx, e runtime.Event, token, owner string) error {
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_leases WHERE token=? AND task_id=? AND owner=? AND writer=0 AND released=1)`, token, e.TaskID, owner).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return runtime.ErrExecutionLeaseLost
	}
	return nil
}

func uniqueWorkerLease(ctx context.Context, tx *sql.Tx, e runtime.Event, token, owner string) error {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN typeof(token)='text' AND length(CAST(token AS BLOB)) BETWEEN 1 AND 512 THEN token END,writer FROM resource_leases WHERE task_id=? AND owner=? LIMIT 2`, e.TaskID, owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var stored sql.NullString
		var writer int
		if err := rows.Scan(&stored, &writer); err != nil {
			return err
		}
		if count != 1 || !stored.Valid || stored.String != token || writer != 0 {
			return runtime.ErrExecutionLeaseLost
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return runtime.ErrExecutionLeaseLost
	}
	return rows.Close()
}

func releaseFinishedWorker(ctx context.Context, tx *sql.Tx, e runtime.Event, token, owner string) error {
	result, err := tx.ExecContext(ctx, `UPDATE resource_leases SET released=1 WHERE token=? AND task_id=? AND owner=? AND writer=0 AND released=0`, token, e.TaskID, owner)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return runtime.ErrExecutionLeaseLost
	}
	return nil
}
