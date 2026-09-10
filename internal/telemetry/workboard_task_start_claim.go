package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// CommitTaskStartClaim is the trusted-host admission boundary for a workboard
// worker. The first runtime event and every claim projection become visible in
// the same SQLite commit. It never dispatches work.
func (s *Store) CommitTaskStartClaim(ctx context.Context, command workboard.TaskStartClaim) (workboard.OperationReceipt, error) {
	if s == nil || ctx == nil || command.Validate() != nil || validateLifecycleMutation(command.Claim, true) != nil {
		return workboard.OperationReceipt{}, invalidWorkboard("task_start_claim")
	}
	body, err := command.Event.Encode()
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	requestDigest, err := workboard.LifecycleDigest(command.Claim)
	if err != nil || requestDigest != command.Claim.RequestDigest {
		return workboard.OperationReceipt{}, invalidWorkboard("request_digest")
	}
	keyDigest := digestBytes([]byte(command.Claim.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	receipt, claimFound, err := readLifecycleReplay(ctx, tx, command.Claim, keyDigest, requestDigest)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	startFound, err := validateTaskStartReplay(ctx, tx, command.Event, body)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if claimFound || startFound {
		if !claimFound || !startFound {
			return workboard.OperationReceipt{}, ErrConflict
		}
		if err = validateTaskStartClaimRecord(ctx, tx, command, receipt, body); err != nil {
			return workboard.OperationReceipt{}, err
		}
		if err = validateExecutionAdmissionReplay(ctx, tx, command, receipt, body); err != nil {
			return workboard.OperationReceipt{}, err
		}
		if err = tx.Commit(); err != nil {
			return workboard.OperationReceipt{}, err
		}
		return receipt, nil
	}
	if err = appendTaskStartTx(ctx, tx, command.Event, body); err != nil {
		return workboard.OperationReceipt{}, err
	}
	receipt, err = applyLifecycleMutationTx(ctx, tx, command.Claim, keyDigest, requestDigest)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertTaskStartClaimRecord(ctx, tx, command, receipt, body); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertExecutionAdmission(ctx, tx, command, receipt, body); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func taskStartClaimRecord(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim, receipt workboard.OperationReceipt, eventBody []byte) (workboard.TaskStartClaimRecord, error) {
	var attemptID, claimID string
	var count int
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id,id FROM workboard_claims
		WHERE board_id=? AND card_id=? AND owner_id=? AND task_id=? LIMIT 2`, command.Claim.BoardID, command.Claim.CardID, command.Claim.Actor.ID, command.Event.TaskID)
	if err != nil {
		return workboard.TaskStartClaimRecord{}, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&attemptID, &claimID); err != nil {
			return workboard.TaskStartClaimRecord{}, err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return workboard.TaskStartClaimRecord{}, err
	}
	record := workboard.TaskStartClaimRecord{Version: 1, TaskID: command.Event.TaskID, SessionID: command.Event.SessionID,
		EventID: command.Event.ID, EventDigest: streamBodyDigest(eventBody), BoardID: command.Claim.BoardID, CardID: command.Claim.CardID,
		AttemptID: attemptID, ClaimID: claimID, WorkerID: command.Claim.Actor.ID, OperationID: receipt.OperationID,
		RequestDigest: command.Claim.RequestDigest, ExpectedCardRevision: command.Claim.ExpectedCardRevision,
		PolicyDigest: command.Claim.PolicyDigest, LeaseTTLNS: int64(command.Claim.LeaseTTL), CreatedAt: command.Event.Time.UTC()}
	if count != 1 || record.Validate() != nil {
		return workboard.TaskStartClaimRecord{}, ErrWorkboardCorrupt
	}
	return record, nil
}

func insertTaskStartClaimRecord(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim, receipt workboard.OperationReceipt, eventBody []byte) error {
	record, err := taskStartClaimRecord(ctx, tx, command, receipt, eventBody)
	if err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_task_start_claims(task_id,session_id,event_id,event_digest,board_id,card_id,attempt_id,claim_id,worker_id,
		operation_id,request_digest,expected_card_revision,policy_digest,lease_ttl_ns,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.TaskID, record.SessionID, record.EventID, record.EventDigest, record.BoardID, record.CardID, record.AttemptID, record.ClaimID,
		record.WorkerID, record.OperationID, record.RequestDigest, record.ExpectedCardRevision, record.PolicyDigest, record.LeaseTTLNS,
		record.CreatedAt.UnixNano(), body)
	return err
}

func validateTaskStartClaimRecord(ctx context.Context, tx *sql.Tx, command workboard.TaskStartClaim, receipt workboard.OperationReceipt, eventBody []byte) error {
	want, err := taskStartClaimRecord(ctx, tx, command, receipt, eventBody)
	if err != nil {
		return err
	}
	var indexed workboard.TaskStartClaimRecord
	var createdAt int64
	var body []byte
	err = tx.QueryRowContext(ctx, `SELECT task_id,session_id,event_id,event_digest,board_id,card_id,attempt_id,claim_id,worker_id,operation_id,
		request_digest,expected_card_revision,policy_digest,lease_ttl_ns,created_at,body FROM workboard_task_start_claims WHERE task_id=?`, command.Event.TaskID).
		Scan(&indexed.TaskID, &indexed.SessionID, &indexed.EventID, &indexed.EventDigest, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID,
			&indexed.ClaimID, &indexed.WorkerID, &indexed.OperationID, &indexed.RequestDigest, &indexed.ExpectedCardRevision,
			&indexed.PolicyDigest, &indexed.LeaseTTLNS, &createdAt, &body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, createdAt).UTC()
	var canonical workboard.TaskStartClaimRecord
	if strictJSON(body, &canonical) != nil || canonical.Validate() != nil || canonical != indexed || canonical != want {
		return ErrWorkboardCorrupt
	}
	return nil
}

// validateTaskStartReplay distinguishes an absent runtime half from an exact,
// projection-consistent retry. An existing task slot or reused event identity
// is never treated as a fresh admission.
func validateTaskStartReplay(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte) (bool, error) {
	var storedID string
	var storedBody []byte
	err := tx.QueryRowContext(ctx, `SELECT id,body FROM events WHERE task_id=? AND sequence=1`, event.TaskID).Scan(&storedID, &storedBody)
	if errors.Is(err, sql.ErrNoRows) {
		var taskCollision, eventCollision bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_heads WHERE task_id=?),
			EXISTS(SELECT 1 FROM events WHERE id=?)`, event.TaskID, event.ID).Scan(&taskCollision, &eventCollision); err != nil {
			return false, err
		}
		if taskCollision || eventCollision {
			return false, ErrConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedID != event.ID || !bytes.Equal(storedBody, body) || validateEventLogRetry(ctx, tx, event, body) != nil ||
		validateSubmissionStreamRetry(ctx, tx, event, body, "") != nil {
		return true, ErrConflict
	}
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, event.TaskID, &events)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessions.ErrHistory) {
			return true, ErrConflict
		}
		return true, err
	}
	if snapshot.SessionID != event.SessionID || snapshot.Sequence < 1 || len(events) != int(snapshot.Sequence) {
		return true, ErrConflict
	}
	for _, current := range events {
		if current.WorkerID != event.WorkerID || current.CorrelationID != event.TaskID {
			return true, ErrConflict
		}
		canonical, encodeErr := current.Encode()
		if encodeErr != nil {
			return true, ErrConflict
		}
		var currentBody []byte
		if err = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=? AND id=?`,
			current.TaskID, current.Sequence, current.ID).Scan(&currentBody); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return true, ErrConflict
			}
			return true, err
		}
		if !bytes.Equal(currentBody, canonical) || validateEventLogRetry(ctx, tx, current, currentBody) != nil ||
			validateSubmissionStreamRetry(ctx, tx, current, currentBody, "") != nil {
			return true, ErrConflict
		}
	}
	if err = validateTaskStartReplayTiming(ctx, tx, event, snapshot.State, events[len(events)-1]); err != nil {
		return true, err
	}
	return true, nil
}

func validateTaskStartReplayTiming(ctx context.Context, tx *sql.Tx, start runtime.Event, state string, head runtime.Event) error {
	var timing taskTiming
	err := tx.QueryRowContext(ctx, `SELECT started_at,terminal_event_id,terminal_sequence,state,duration_ns,reason
		FROM task_timings WHERE task_id=?`, start.TaskID).Scan(&timing.started, &timing.terminal, &timing.sequence, &timing.state, &timing.duration, &timing.reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if timing.validate() != nil || timing.state != state || !timing.started.Valid ||
		timing.started.String != start.Time.UTC().Format(time.RFC3339Nano) {
		return ErrConflict
	}
	if state == "running" {
		return nil
	}
	if !timing.terminal.Valid || timing.terminal.String != head.ID || !timing.sequence.Valid || timing.sequence.Int64 != head.Sequence {
		return ErrConflict
	}
	duration := head.Time.Sub(start.Time)
	if duration >= 0 && start.Time.Add(duration).Equal(head.Time) {
		if timing.reason != "observed" || !timing.duration.Valid || timing.duration.Int64 != int64(duration) {
			return ErrConflict
		}
	} else if timing.reason != "invalid_time" || timing.duration.Valid {
		return ErrConflict
	}
	return nil
}

func appendTaskStartTx(ctx context.Context, tx *sql.Tx, event runtime.Event, body []byte) error {
	if event.Kind != runtime.TaskStarted || event.Sequence != 1 || event.Data.SubmissionID != "" || event.Validate() != nil {
		return ErrConflict
	}
	if event.Data.Compaction != nil && event.Data.Compaction.SummaryAttemptID != "" {
		if err := validateSummaryGate(ctx, tx, event.Data.Compaction); err != nil {
			return err
		}
	}
	if err := validatePostCompactionJournalBudget(ctx, tx, event, body); err != nil {
		return err
	}
	if err := submissionAppendGate(ctx, tx, event, "", ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?,?,0,'running')`, event.TaskID, event.SessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,?,?)`, event.ID, event.TaskID, event.Sequence, body); err != nil {
		return err
	}
	if err := appendEventLog(ctx, tx, event, body); err != nil {
		return err
	}
	if err := appendSubmissionStreamEvent(ctx, tx, event, body, ""); err != nil {
		return err
	}
	if err := appendSkillExposures(ctx, tx, event); err != nil {
		return err
	}
	if err := appendTaskTiming(ctx, tx, event); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_heads SET sequence=1 WHERE task_id=? AND session_id=? AND sequence=0 AND state='running'`, event.TaskID, event.SessionID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}
