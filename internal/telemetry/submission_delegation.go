package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// RecoverInterruptedDelegation closes a provably completed delegated tool pair
// and fails its interrupted parent. It never dispatches work or releases leases.
// Journal repair, submission fencing and its receipt share one writer transaction.
func (s *Store) RecoverInterruptedDelegation(ctx context.Context, id, configDigest string, now time.Time) (bool, error) {
	commit, err := s.RecoverInterruptedDelegationCommit(ctx, id, configDigest, now)
	return commit.Changed, err
}

// RecoverInterruptedModel resolves expired model-only execution as failed or
// canceled. It preserves partial stream events without publishing an answer,
// retrying generation, or granting continuation of an incomplete turn.
func (s *Store) RecoverInterruptedModel(ctx context.Context, id, configDigest string, now time.Time) (bool, error) {
	commit, err := s.RecoverInterruptedModelCommit(ctx, id, configDigest, now)
	return commit.Changed, err
}

// SubmissionRecoveryCommit describes only runtime events newly committed by
// this invocation. A false Changed result always has an empty event set.
type SubmissionRecoveryCommit struct {
	Changed bool
	Events  []runtime.Event
}

func (s *Store) RecoverInterruptedDelegationCommit(ctx context.Context, id, configDigest string, now time.Time) (SubmissionRecoveryCommit, error) {
	return s.RecoverInterruptedDelegationCommitScreened(ctx, id, configDigest, now, nil)
}

func (s *Store) RecoverInterruptedModelCommit(ctx context.Context, id, configDigest string, now time.Time) (SubmissionRecoveryCommit, error) {
	return s.RecoverInterruptedModelCommitScreened(ctx, id, configDigest, now, nil)
}

func (s *Store) RecoverInterruptedDelegationCommitScreened(ctx context.Context, id, configDigest string, now time.Time, secrets []string) (SubmissionRecoveryCommit, error) {
	return s.recoverInterruptedSubmissionCommit(ctx, id, configDigest, now, "interrupted_delegation", 2, sessions.PlanInterruptedDelegation, secrets)
}

func (s *Store) RecoverInterruptedModelCommitScreened(ctx context.Context, id, configDigest string, now time.Time, secrets []string) (SubmissionRecoveryCommit, error) {
	return s.recoverInterruptedSubmissionCommit(ctx, id, configDigest, now, "interrupted_model", 1, sessions.PlanInterruptedModel, secrets)
}

func (s *Store) recoverInterruptedSubmissionCommit(ctx context.Context, id, configDigest string, now time.Time, reason string, appended int64, planner func([][]runtime.Event, time.Time, bool) (sessions.InterruptionRecovery, error), secrets []string) (SubmissionRecoveryCommit, error) {
	var committed []runtime.Event
	changed, err := s.recoverInterruptedSubmission(ctx, id, configDigest, now, reason, appended, planner, secrets, &committed)
	if err != nil || !changed {
		return SubmissionRecoveryCommit{}, err
	}
	return SubmissionRecoveryCommit{Changed: true, Events: committed}, nil
}

func (s *Store) recoverInterruptedSubmission(ctx context.Context, id, configDigest string, now time.Time, reason string, appended int64, planner func([][]runtime.Event, time.Time, bool) (sessions.InterruptionRecovery, error), secrets []string, committed *[]runtime.Event) (bool, error) {
	if !sessions.ValidEventPageID(id) || !submissionDigest(configDigest) || now.IsZero() {
		return false, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, config, token, expiry string
	var canceled int
	err = tx.QueryRowContext(ctx, `SELECT state,config_digest,token,lease_expires_at,cancel_requested FROM submissions WHERE id=? AND length(CAST(state AS BLOB))<=16 AND length(CAST(config_digest AS BLOB))=64 AND length(CAST(token AS BLOB))<=128 AND length(CAST(lease_expires_at AS BLOB))<=64`, id).Scan(&state, &config, &token, &expiry, &canceled)
	if err != nil {
		return false, err
	}
	if state != "running" || config != configDigest {
		return false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !sessions.ValidEventPageID(token) || canceled < 0 || canceled > 1 {
		return false, submissions.ErrInvalid
	}
	if now.Before(at) {
		return false, nil
	}
	if err = validateBranchRecoveryTx(ctx, tx, id); err != nil {
		return false, err
	}
	histories, err := interruptedSubmissionHistories(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if len(histories) == 0 {
		return false, nil
	}
	plan, err := planner(histories, now, canceled == 1)
	if errors.Is(err, sessions.ErrHistory) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if int64(len(plan.Events)) != appended {
		return false, submissions.ErrInvalid
	}
	var parent []runtime.Event
	for _, history := range histories {
		if history[0].TaskID == plan.ParentTaskID {
			parent = history
		}
	}
	if len(parent) == 0 || parent[len(parent)-1].Sequence != plan.ExpectedSequence {
		return false, submissions.ErrInvalid
	}
	var parentCanceled bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_cancellations WHERE task_id=?)`, plan.ParentTaskID).Scan(&parentCanceled); err != nil {
		return false, err
	}
	if parentCanceled && canceled == 0 {
		canceled = 1
		plan, err = planner(histories, now, true)
		if err != nil {
			return false, err
		}
	}
	var continuation sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE
		WHEN json_type(request,'$.request.ContinueTaskID') IS NULL THEN ''
		WHEN json_type(request,'$.request.ContinueTaskID')='text' AND length(CAST(json_extract(request,'$.request.ContinueTaskID') AS BLOB))<=128
		THEN json_extract(request,'$.request.ContinueTaskID') END FROM submissions WHERE id=?`, id).Scan(&continuation)
	if err != nil {
		return false, err
	}
	if !continuation.Valid || (continuation.String != "" && !sessions.ValidEventPageID(continuation.String)) || parent[0].Data.ParentTaskID != continuation.String {
		return false, submissions.ErrInvalid
	}
	if recoveryEventsContainSecrets(plan.Events, secrets) {
		return false, ErrRecoveryRedaction
	}
	// Validate the proposed journal through the same replay path as ordinary reads.
	combined := append(append(snapshotEvents(nil), parent...), plan.Events...)
	projection, err := sessions.Replay(ctx, combined, plan.ParentTaskID)
	if err != nil {
		return false, err
	}
	action, code := "failed", "execution_failed"
	if canceled == 1 {
		action, code = "canceled", "canceled"
	}
	if projection.State != action || projection.Sequence != plan.ExpectedSequence+appended {
		return false, submissions.ErrInvalid
	}
	for i, event := range plan.Events {
		if event.TaskID != plan.ParentTaskID || event.SessionID != parent[0].SessionID || event.Sequence != plan.ExpectedSequence+int64(i)+1 {
			return false, submissions.ErrInvalid
		}
		body, err := event.Encode()
		if err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events VALUES(?,?,?,?)`, event.ID, event.TaskID, event.Sequence, body); err != nil {
			return false, err
		}
		if err = appendEventLog(ctx, tx, event, body); err != nil {
			return false, err
		}
		if err = appendSubmissionStreamEvent(ctx, tx, event, body, id); err != nil {
			return false, err
		}
		if err = appendTaskTiming(ctx, tx, event); err != nil {
			return false, err
		}
	}
	// Include new records in the raw on-disk budget (unknown JSON metadata is
	// counted too), not only the planner's canonical decoded-event budget.
	var totalBytes, totalEvents int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(body AS BLOB))),0),count(*) FROM events WHERE task_id IN (SELECT task_id FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=?)`, id).Scan(&totalBytes, &totalEvents)
	if err != nil {
		return false, err
	}
	if totalBytes > 8<<20 || totalEvents > 10000 {
		return false, submissions.ErrInvalid
	}
	changed, err := tx.ExecContext(ctx, `UPDATE task_heads SET sequence=?,state=? WHERE task_id=? AND session_id=? AND sequence=? AND state='running'`, projection.Sequence, action, plan.ParentTaskID, parent[0].SessionID, plan.ExpectedSequence)
	if err != nil {
		return false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return false, ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM submission_recoveries WHERE submission_id=?`, id).Scan(&count); err != nil {
		return false, err
	}
	if count >= 4 {
		return false, submissions.ErrInvalid
	}
	receipt := submissions.Recovery{Version: 1, ID: rand.Text(), SubmissionID: id, Time: now.UTC(), Action: action, Reason: reason}
	if receipt.Validate() != nil {
		return false, submissions.ErrInvalid
	}
	audit, err := json.Marshal(receipt)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256([]byte(token))
	if _, err = tx.ExecContext(ctx, `INSERT INTO submission_recoveries VALUES(?,?,?,?)`, receipt.ID, id, hex.EncodeToString(digest[:]), audit); err != nil {
		return false, err
	}
	result, err := json.Marshal(submissions.Result{TaskID: plan.ParentTaskID, AuditStatus: "not_recovered", PreviousTaskIDs: []string{}})
	if err != nil {
		return false, err
	}
	changed, err = tx.ExecContext(ctx, `UPDATE submissions SET state=?,token='',lease_expires_at='',updated_at=?,result=?,error_code=? WHERE id=? AND state='running' AND token=? AND config_digest=? AND lease_expires_at=?`, action, submissionTime(now), result, code, id, token, configDigest, expiry)
	if err != nil {
		return false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return false, submissions.ErrLeaseLost
	}
	detached := make([]runtime.Event, len(plan.Events))
	for i := range plan.Events {
		detached[i], err = plan.Events[i].Clone()
		if err != nil {
			return false, submissions.ErrInvalid
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	*committed = detached
	return true, nil
}

func interruptedSubmissionHistories(ctx context.Context, tx *sql.Tx, id string) ([][]runtime.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB))<=128 THEN task_id END FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=? ORDER BY rowid LIMIT 67`, id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var task sql.NullString
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return nil, err
		}
		if !task.Valid || !sessions.ValidEventPageID(task.String) {
			rows.Close()
			return nil, submissions.ErrInvalid
		}
		ids = append(ids, task.String)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || len(ids) > 66 {
		return nil, nil
	}
	histories := make([][]runtime.Event, 0, len(ids))
	var bytes, count int64
	for _, id := range ids {
		var size, events int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(body AS BLOB))),0),count(*) FROM events WHERE task_id=?`, id).Scan(&size, &events); err != nil {
			return nil, err
		}
		if size < 1 || events < 1 || size > (8<<20)-bytes || events > 10000-count {
			return nil, submissions.ErrInvalid
		}
		bytes += size
		count += events
		var history []runtime.Event
		if _, err = taskSnapshotWithEvents(ctx, tx, id, &history); err != nil {
			return nil, err
		}
		histories = append(histories, history)
	}
	for _, history := range histories {
		if history[0].Data.SubmissionID != id {
			return nil, submissions.ErrInvalid
		}
	}
	return histories, nil
}
