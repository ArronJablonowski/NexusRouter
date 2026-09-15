package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrContextCompactionRecovery = errors.New("context compaction recovery unavailable")

type contextCompactionRecoveryCandidate struct {
	RowID       int64
	OperationID string
	StartBody   []byte
	Start       sessions.ContextCompactionPlanStart
	ProcessID   string
	Reference   processguard.Reference
}

// ReconcileContextCompactionPlansPage conservatively terminates operations
// whose exact guarded local owner is independently proven stopped. Held and
// unverifiable owners are skipped. It never repeats summary inference.
func (s *Store) ReconcileContextCompactionPlansPage(ctx context.Context, after string, limit int, now time.Time) (next string, recovered int, err error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return "", 0, ErrContextCompactionRecovery
	}
	now = now.UTC()
	if now.IsZero() || now.Year() < 1970 || now.Year() >= 2261 {
		return "", 0, ErrContextCompactionRecovery
	}
	var cursor int64
	if after != "" {
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil || cursor < 1 || strconv.FormatInt(cursor, 10) != after {
			return "", 0, ErrContextCompactionRecovery
		}
	}
	recovering, err := processguard.Current(ctx)
	if err != nil || recovering.Validate() != nil {
		return after, 0, ErrContextCompactionRecovery
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(bounded, `SELECT operation.rowid,operation.operation_id,
		CASE WHEN length(CAST(operation.body AS BLOB)) BETWEEN 1 AND 16777216 THEN operation.body END,
		operation.process_id,CASE WHEN length(CAST(process.body AS BLOB)) BETWEEN 1 AND 8192 THEN process.body END
		FROM context_compaction_operations operation
		JOIN lease_processes process ON process.id=operation.process_id
		JOIN summary_attempts attempt ON attempt.id=operation.attempt_id AND attempt.task_id=operation.task_id
		WHERE operation.rowid>? AND NOT EXISTS(SELECT 1 FROM context_compaction_plan_recoveries recovery WHERE recovery.operation_id=operation.operation_id)
		AND (SELECT fact.kind FROM context_compaction_plan_facts fact WHERE fact.operation_id=operation.operation_id ORDER BY fact.sequence DESC LIMIT 1)
			='started' AND json_extract(attempt.body,'$.Status') IN('started','interrupted')
		ORDER BY operation.rowid LIMIT ?`, cursor, limit)
	if err != nil {
		return after, 0, fmt.Errorf("%w: list: %v", ErrContextCompactionRecovery, err)
	}
	candidates := []contextCompactionRecoveryCandidate{}
	for rows.Next() {
		var candidate contextCompactionRecoveryCandidate
		var referenceBody []byte
		if rows.Scan(&candidate.RowID, &candidate.OperationID, &candidate.StartBody, &candidate.ProcessID, &referenceBody) != nil ||
			candidate.RowID <= cursor || len(candidate.StartBody) == 0 || len(referenceBody) == 0 ||
			json.Unmarshal(candidate.StartBody, &candidate.Start) != nil || candidate.Start.Validate() != nil ||
			candidate.Start.OperationID != candidate.OperationID || candidate.Start.ProcessID != candidate.ProcessID ||
			json.Unmarshal(referenceBody, &candidate.Reference) != nil || candidate.Reference.Validate() != nil || candidate.Reference.ID != candidate.ProcessID {
			rows.Close()
			return after, 0, ErrContextCompactionRecovery
		}
		candidates = append(candidates, candidate)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return after, 0, ErrContextCompactionRecovery
	}
	next = after
	for _, candidate := range candidates {
		next = strconv.FormatInt(candidate.RowID, 10)
		if candidate.ProcessID == recovering.ID {
			continue
		}
		observation, probeErr := processguard.Probe(bounded, candidate.Reference)
		if probeErr != nil {
			if bounded.Err() != nil {
				return next, recovered, bounded.Err()
			}
			continue
		}
		if observation.State != processguard.Unlocked || observation.ConfirmUnlocked(bounded) != nil {
			if closeErr := observation.Close(); closeErr != nil {
				return next, recovered, ErrContextCompactionRecovery
			}
			continue
		}
		changed, recoveryErr := s.recoverContextCompactionCandidate(bounded, candidate, recovering, observation, now)
		closeErr := observation.Close()
		if recoveryErr != nil || closeErr != nil {
			return next, recovered, fmt.Errorf("%w: commit", ErrContextCompactionRecovery)
		}
		if changed {
			recovered++
		}
	}
	if len(candidates) < limit {
		next = ""
	}
	return next, recovered, nil
}

func (s *Store) recoverContextCompactionCandidate(ctx context.Context, candidate contextCompactionRecoveryCandidate, recovering processguard.Reference, observation *processguard.Observation, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if result, updateErr := tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", candidate.Start.TaskID); updateErr != nil {
		return false, updateErr
	} else if count, countErr := result.RowsAffected(); countErr != nil || count != 1 || observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrContextCompactionRecovery
	}
	if recovering.ID == candidate.ProcessID || registerLeaseProcess(ctx, tx, recovering) != nil {
		return false, ErrContextCompactionRecovery
	}
	var currentBody, processBody []byte
	var currentProcess string
	if err = tx.QueryRowContext(ctx, `SELECT operation.body,operation.process_id,process.body
		FROM context_compaction_operations operation JOIN lease_processes process ON process.id=operation.process_id
		WHERE operation.operation_id=?`, candidate.OperationID).Scan(&currentBody, &currentProcess, &processBody); err != nil {
		return false, err
	}
	wantProcess, err := json.Marshal(candidate.Reference)
	if err != nil || !bytes.Equal(currentBody, candidate.StartBody) || currentProcess != candidate.ProcessID || !bytes.Equal(processBody, wantProcess) || observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrContextCompactionRecovery
	}
	state, err := readContextCompactionPlanState(ctx, tx, candidate.OperationID)
	if err != nil {
		return false, err
	}
	latest := state.Facts[len(state.Facts)-1]
	switch latest.Kind {
	case sessions.ContextCompactionRevoked, sessions.ContextCompactionActivated, sessions.ContextCompactionFailed:
		var existing string
		if receiptErr := tx.QueryRowContext(ctx, "SELECT recovery_id FROM context_compaction_plan_recoveries WHERE operation_id=?", candidate.OperationID).Scan(&existing); receiptErr == nil {
			return false, tx.Commit()
		}
		return false, nil
	case sessions.ContextCompactionStarted:
		// Only uncertain in-flight summary inference is recoverable. A durable
		// drafted result, prepared plan, validation or approval survives its
		// original process and remains available to a later operator/process.
		var attemptBody []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM summary_attempts WHERE id=? AND task_id=?", state.Start.AttemptID, state.Start.TaskID).Scan(&attemptBody); err != nil {
			return false, err
		}
		attempt, decodeErr := decodeSummaryAttempt(attemptBody, state.Start.AttemptID, state.Start.TaskID)
		if decodeErr != nil {
			return false, ErrContextCompactionRecovery
		}
		if attempt.Status != "started" && attempt.Status != "interrupted" {
			return false, tx.Commit()
		}
	case sessions.ContextCompactionPrepared, sessions.ContextCompactionValidated, sessions.ContextCompactionApproved:
		return false, tx.Commit()
	default:
		return false, ErrContextCompactionRecovery
	}
	if err = s.interruptContextCompactionSummary(ctx, tx, state.Start, now); err != nil {
		return false, err
	}
	factID := contextCompactionRecoveryID("fact", state.Start.OperationDigest, latest.Digest)
	fact := sessions.ContextCompactionLifecycleFact{ID: factID, OperationID: state.Start.OperationID, Sequence: latest.Sequence + 1,
		PreviousID: latest.ID, Kind: sessions.ContextCompactionFailed, Code: "owner_interrupted", CreatedAt: now}
	if state.Plan != nil {
		fact.PlanDigest, fact.SummaryAttemptID, fact.SummaryReviewID = state.Plan.PlanDigest, state.Plan.Compaction.SummaryAttemptID, state.Plan.Compaction.SummaryReviewID
	}
	fact, err = sessions.SealContextCompactionLifecycleFact(fact)
	if err != nil || sessions.ValidateContextCompactionTransition(&latest, fact) != nil {
		return false, ErrContextCompactionRecovery
	}
	factBody, err := json.Marshal(fact)
	if err != nil || insertContextCompactionFact(ctx, tx, fact, factBody) != nil {
		return false, ErrContextCompactionRecovery
	}
	recovery := sessions.ContextCompactionRecovery{ID: contextCompactionRecoveryID("receipt", state.Start.OperationDigest, fact.Digest),
		OperationID: state.Start.OperationID, ProcessID: recovering.ID, FailedFactID: fact.ID, FailedFactDigest: fact.Digest, RecoveredAt: now}
	recovery, err = sessions.SealContextCompactionRecovery(recovery)
	if err != nil {
		return false, ErrContextCompactionRecovery
	}
	recoveryBody, err := json.Marshal(recovery)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_compaction_plan_recoveries
		(recovery_id,recovery_digest,operation_id,process_id,failed_fact_id,failed_fact_digest,reason,recovered_at,body)
		VALUES(?,?,?,?,?,?,?,?,?)`, recovery.ID, recovery.Digest, recovery.OperationID, recovery.ProcessID, recovery.FailedFactID,
		recovery.FailedFactDigest, recovery.Reason, recovery.RecoveredAt.Format(time.RFC3339Nano), recoveryBody); err != nil {
		return false, err
	}
	if observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrContextCompactionRecovery
	}
	final, err := readContextCompactionPlanState(ctx, tx, candidate.OperationID)
	if err != nil || final.Status != sessions.ContextCompactionFailed || final.Recovery == nil || final.Recovery.Digest != recovery.Digest {
		return false, ErrContextCompactionRecovery
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// If inference was in flight, close it using the existing summary recovery
// evidence in this same transaction. Drafted/failed attempts are immutable and
// are retained without promoting their output.
func (s *Store) interruptContextCompactionSummary(ctx context.Context, tx *sql.Tx, start sessions.ContextCompactionPlanStart, now time.Time) error {
	current, err := readSummaryRecoveryCandidate(ctx, tx, start.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		var body []byte
		var processID sql.NullString
		if readErr := tx.QueryRowContext(ctx, "SELECT body,process_id FROM summary_attempts WHERE id=? AND task_id=?", start.AttemptID, start.TaskID).Scan(&body, &processID); readErr != nil {
			return readErr
		}
		attempt, decodeErr := decodeSummaryAttempt(body, start.AttemptID, start.TaskID)
		if decodeErr != nil || !processID.Valid || processID.String != start.ProcessID || attempt.Status == "started" {
			return ErrContextCompactionRecovery
		}
		if attempt.Status == "interrupted" {
			var id, taskID string
			var recoveredAt int64
			var receiptBody []byte
			if receiptErr := tx.QueryRowContext(ctx, `SELECT id,task_id,recovered_at,body FROM summary_attempt_recoveries WHERE attempt_id=?`, start.AttemptID).Scan(&id, &taskID, &recoveredAt, &receiptBody); receiptErr != nil {
				return ErrContextCompactionRecovery
			}
			if receipt, receiptErr := decodeSummaryRecovery(receiptBody, id, start.AttemptID, taskID, recoveredAt); receiptErr != nil || receipt.Code != "owner_interrupted" {
				return ErrContextCompactionRecovery
			}
		}
		return nil
	}
	if err != nil || current.ProcessID != start.ProcessID || current.Attempt.SourceSequence != start.SourceSequence ||
		current.Attempt.SourceDigest != start.SourceDigest || current.Attempt.Model != start.Model || current.Attempt.Provider != start.Provider ||
		current.Attempt.Keep != start.Keep || current.Attempt.EstimatedCost != start.EstimatedCost || !current.Attempt.StartedAt.Equal(start.StartedAt) {
		return ErrContextCompactionRecovery
	}
	terminal := current.Attempt
	terminal.Status, terminal.Code, terminal.FinishedAt = "interrupted", "owner_interrupted", now
	terminal.Draft, terminal.Usage, terminal.Elapsed = nil, nil, 0
	if terminal.Validate() != nil {
		return ErrContextCompactionRecovery
	}
	terminalBody, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE summary_attempts SET body=? WHERE id=? AND task_id=? AND process_id=? AND body=?`,
		terminalBody, current.AttemptID, current.TaskID, current.ProcessID, current.Body)
	if err != nil {
		return err
	}
	if count, countErr := updated.RowsAffected(); countErr != nil || count != 1 {
		return ErrContextCompactionRecovery
	}
	if err = appendSummaryUsage(ctx, tx, terminal); err != nil {
		return err
	}
	receipt := summaryRecoveryReceipt(current, now)
	receiptBody, err := json.Marshal(receipt)
	if err != nil || receipt.Validate() != nil || len(receiptBody) > 4096 {
		return ErrContextCompactionRecovery
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO summary_attempt_recoveries(id,attempt_id,task_id,recovered_at,body) VALUES(?,?,?,?,?)`,
		receipt.ID, receipt.AttemptID, receipt.TaskID, receipt.RecoveredAt.UnixNano(), receiptBody)
	return err
}

func contextCompactionRecoveryID(kind, operationDigest, factDigest string) string {
	digest := sha256.Sum256([]byte("darwin-context-compaction-recovery-v1\x00" + kind + "\x00" + operationDigest + "\x00" + factDigest))
	return "cpr_" + hex.EncodeToString(digest[:])
}

func (s *Store) ContextCompactionRecovery(ctx context.Context, operationID string) (sessions.ContextCompactionRecovery, error) {
	var id, digest, processID, failedID, failedDigest, reason, recoveredAt string
	var body []byte
	if ctx == nil || operationID == "" || len(operationID) > 128 {
		return sessions.ContextCompactionRecovery{}, sessions.ErrContextCompactionLifecycle
	}
	err := s.db.QueryRowContext(ctx, `SELECT recovery_id,recovery_digest,process_id,failed_fact_id,failed_fact_digest,reason,recovered_at,body
		FROM context_compaction_plan_recoveries WHERE operation_id=?`, operationID).Scan(&id, &digest, &processID, &failedID, &failedDigest, &reason, &recoveredAt, &body)
	if err != nil {
		return sessions.ContextCompactionRecovery{}, err
	}
	var recovery sessions.ContextCompactionRecovery
	if json.Unmarshal(body, &recovery) != nil || recovery.Validate() != nil || recovery.ID != id || recovery.Digest != digest ||
		recovery.OperationID != operationID || recovery.ProcessID != processID || recovery.FailedFactID != failedID ||
		recovery.FailedFactDigest != failedDigest || recovery.Reason != reason || recovery.RecoveredAt.Format(time.RFC3339Nano) != recoveredAt {
		return sessions.ContextCompactionRecovery{}, sessions.ErrContextCompactionLifecycle
	}
	return recovery, nil
}
