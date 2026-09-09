package telemetry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func readSteering(ctx context.Context, tx *sql.Tx, task, id string) (runtime.SteeringMessage, error) {
	m := runtime.SteeringMessage{Version: 1, ID: id, TaskID: task}
	var created string
	var sequence sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT text,state,created_at,applied_sequence FROM task_steering WHERE task_id=? AND id=? AND length(CAST(text AS BLOB))<=65536 AND length(CAST(state AS BLOB))<=16 AND length(CAST(created_at AS BLOB))<=64`, task, id).Scan(&m.Text, &m.State, &created, &sequence)
	if err != nil {
		return m, err
	}
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return m, sessions.ErrHistory
	}
	if sequence.Valid {
		m.AppliedSequence = &sequence.Int64
	}
	if m.Validate() != nil {
		return m, sessions.ErrHistory
	}
	return m, nil
}

func (s *Store) QueueSteering(ctx context.Context, task, keyHash, text string) (runtime.SteeringMessage, error) {
	return s.QueueSteeringAtRevision(ctx, task, keyHash, text, 0)
}

// QueueSteeringAtRevision binds a new message to an observed task head. Exact
// key retries are resolved first so later task progress cannot invalidate an
// acknowledgement-loss replay.
func (s *Store) QueueSteeringAtRevision(ctx context.Context, task, keyHash, text string, expected int64) (runtime.SteeringMessage, error) {
	message := runtime.SteeringMessage{Version: 1, ID: rand.Text(), TaskID: task, Text: text, State: "pending", CreatedAt: time.Now().UTC()}
	if !submissionDigest(keyHash) || message.Validate() != nil || expected < 0 || expected > sessions.MaxTaskEvents {
		return runtime.SteeringMessage{}, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtime.SteeringMessage{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE task_heads SET sequence=sequence WHERE task_id=?`, task); err != nil {
		return runtime.SteeringMessage{}, err
	}
	var priorID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END FROM task_steering WHERE task_id=? AND key_hash=?`, task, keyHash).Scan(&priorID)
	if err == nil {
		if !priorID.Valid {
			return runtime.SteeringMessage{}, sessions.ErrHistory
		}
		prior, e := readSteering(ctx, tx, task, priorID.String)
		if e != nil {
			return runtime.SteeringMessage{}, e
		}
		if prior.Text != text {
			return runtime.SteeringMessage{}, ErrConflict
		}
		return prior, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return runtime.SteeringMessage{}, err
	}
	if expected > 0 {
		var current int64
		if err = tx.QueryRowContext(ctx, "SELECT sequence FROM task_heads WHERE task_id=?", task).Scan(&current); err != nil {
			return runtime.SteeringMessage{}, err
		}
		if current != expected {
			return runtime.SteeringMessage{}, ErrConflict
		}
	}
	status, err := cancellationStatus(ctx, tx, task, false)
	if err != nil {
		return runtime.SteeringMessage{}, err
	}
	if status.State != "running" || status.Requested {
		return runtime.SteeringMessage{}, runtime.ErrSteeringClosed
	}
	count, _, err := steeringCounts(ctx, tx, task)
	if err != nil {
		return runtime.SteeringMessage{}, err
	}
	if count >= 32 {
		return runtime.SteeringMessage{}, runtime.ErrSteeringLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_steering VALUES(?,?,?,?,'pending',?,NULL)`, message.ID, task, keyHash, text, message.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return runtime.SteeringMessage{}, err
	}
	return message, tx.Commit()
}

func (s *Store) NextSteering(ctx context.Context, task string) (*runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) {
		return nil, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version < 14 {
		return nil, nil
	}
	if _, _, err := steeringCounts(ctx, tx, task); err != nil {
		return nil, err
	}
	var id sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END FROM task_steering WHERE task_id=? AND state='pending' ORDER BY rowid LIMIT 1`, task).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !id.Valid {
		return nil, sessions.ErrHistory
	}
	message, err := readSteering(ctx, tx, task, id.String)
	if err != nil {
		return nil, err
	}
	return &message, tx.Commit()
}

func (s *Store) SteeringStatus(ctx context.Context, task, id string) (runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) {
		return runtime.SteeringMessage{}, sessions.ErrHistory
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runtime.SteeringMessage{}, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return runtime.SteeringMessage{}, err
	}
	if version < 14 {
		return runtime.SteeringMessage{}, sql.ErrNoRows
	}
	message, err := readSteering(ctx, tx, task, id)
	if err != nil {
		return runtime.SteeringMessage{}, err
	}
	return message, tx.Commit()
}

func steeringAppendGate(ctx context.Context, tx *sql.Tx, e runtime.Event) error {
	if e.Kind == runtime.TaskCompleted || (e.Kind == runtime.TaskFailed && e.Data.Code == "provider_retryable_no_output") {
		_, pending, err := steeringCounts(ctx, tx, e.TaskID)
		if err != nil {
			return err
		}
		if pending > 0 {
			return runtime.ErrSteeringPending
		}
	}
	if e.Kind != runtime.SteeringApplied {
		return nil
	}
	message, err := readSteering(ctx, tx, e.TaskID, e.Data.SteeringID)
	if err != nil {
		return err
	}
	if message.State != "pending" || message.Text != e.Data.Text {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_steering SET state='applied',applied_sequence=? WHERE task_id=? AND id=? AND state='pending'`, e.Sequence, e.TaskID, e.Data.SteeringID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func steeringCounts(ctx context.Context, tx *sql.Tx, task string) (int, int, error) {
	var total, pending, bad int
	err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN state='pending' THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN (state='pending' AND applied_sequence IS NULL) OR (state='applied' AND applied_sequence>1) THEN 0 ELSE 1 END),0) FROM task_steering WHERE task_id=?`, task).Scan(&total, &pending, &bad)
	if err != nil {
		return 0, 0, err
	}
	if total < 0 || total > 32 || pending < 0 || pending > total || bad != 0 {
		return 0, 0, sessions.ErrHistory
	}
	return total, pending, nil
}
