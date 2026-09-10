package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type executionTerminalProof struct {
	event       runtime.Event
	digest      string
	timeMS      int64
	tokens      int64
	tokensKnown bool
}

// settleExecutionAttempt releases a durable execution reservation only after
// the caller has moved the attempt and claim out of their running states in
// this transaction. Older, deliberately unbudgeted attempts remain valid.
func settleExecutionAttempt(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID, claimID string, expectedKind runtime.Kind, rejectOverrun bool, settledAt time.Time) (int, error) {
	admission, found, err := executionAdmissionForAttempt(ctx, tx, boardID, cardID, attemptID, claimID)
	if err != nil || !found {
		return 0, err
	}
	if _, _, exists, err := readExecutionSettlement(ctx, tx, admission.AdmissionID); err != nil {
		return 0, err
	} else if exists {
		return 0, ErrConflict
	}
	proof, err := readExecutionTerminalProof(ctx, tx, admission)
	if err != nil {
		return 0, err
	}
	if expectedKind != "" && proof.event.Kind != expectedKind {
		return 0, ErrWorkboardCorrupt
	}
	if err = enforceSuccessfulExecutionReservation(admission, proof, rejectOverrun); err != nil {
		return 0, err
	}
	id := newWorkboardID()
	if id == "" {
		return 0, errors.New("secure identifier generation failed")
	}
	record, err := executionSettlementRecord(admission, proof, id, settledAt)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workboard_execution_settlements(settlement_id,admission_id,admission_digest,task_id,session_id,
		board_id,card_id,attempt_id,claim_id,worker_id,operation_id,model_id,provider_id,config_id,terminal_event_id,
		terminal_event_digest,terminal_kind,terminal_sequence,charged_time_ms,charged_tokens,charged_cost_micros,token_usage_known,
		settled_at,settlement_digest,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.SettlementID,
		record.AdmissionID, record.AdmissionDigest, record.TaskID, record.SessionID, record.BoardID, record.CardID, record.AttemptID,
		record.ClaimID, record.WorkerID, record.OperationID, record.ModelID, record.ProviderID, record.ConfigID, record.TerminalEventID,
		record.TerminalEventDigest, record.TerminalKind, record.TerminalSequence, record.ChargedTimeMS, record.ChargedTokens,
		record.ChargedCostMicros, boolInt(record.TokenUsageKnown), record.SettledAt.UnixNano(), record.SettlementDigest, body)
	if err != nil {
		return 0, err
	}
	return len(body), nil
}

// validateExecutionSettlementReplay makes the immutable accounting fact part
// of exact mutation replay. An admitted attempt cannot be replayed if its
// settlement or the runtime evidence supporting it is missing or altered.
func validateExecutionSettlementReplay(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID, claimID string, expectedKind runtime.Kind, rejectOverrun bool) error {
	admission, found, err := executionAdmissionForAttempt(ctx, tx, boardID, cardID, attemptID, claimID)
	if err != nil || !found {
		return err
	}
	indexed, canonical, exists, err := readExecutionSettlement(ctx, tx, admission.AdmissionID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrWorkboardCorrupt
	}
	proof, err := readExecutionTerminalProof(ctx, tx, admission)
	if err != nil {
		return err
	}
	if expectedKind != "" && proof.event.Kind != expectedKind {
		return ErrWorkboardCorrupt
	}
	if err = enforceSuccessfulExecutionReservation(admission, proof, rejectOverrun); err != nil {
		return err
	}
	if indexed != canonical || validateStoredExecutionSettlement(ctx, tx, admission, canonical) != nil {
		return ErrWorkboardCorrupt
	}
	return nil
}

func enforceSuccessfulExecutionReservation(admission workboard.ExecutionAdmissionRecord, proof executionTerminalProof, enforce bool) error {
	if !enforce {
		return nil
	}
	if admission.TimeLimitMS > 0 && proof.timeMS > admission.TimeLimitMS {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "time_budget"}
	}
	if proof.tokensKnown && admission.TokenLimit > 0 && proof.tokens > admission.TokenLimit {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "token_budget"}
	}
	return nil
}

func validateStoredExecutionSettlement(ctx context.Context, tx *sql.Tx, admission workboard.ExecutionAdmissionRecord, settlement workboard.ExecutionSettlementRecord) error {
	finalizedAt, err := executionAttemptFinalizedAt(ctx, tx, admission)
	if err != nil || !settlement.SettledAt.Equal(finalizedAt) {
		return ErrWorkboardCorrupt
	}
	proof, err := readExecutionTerminalProof(ctx, tx, admission)
	if err != nil {
		return err
	}
	want, err := executionSettlementRecord(admission, proof, settlement.SettlementID, finalizedAt)
	if err != nil || settlement != want {
		return ErrWorkboardCorrupt
	}
	return nil
}

func executionAdmissionForAttempt(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID, claimID string) (workboard.ExecutionAdmissionRecord, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM workboard_execution_admissions
		WHERE board_id=? AND card_id=? AND attempt_id=? AND claim_id=? LIMIT 2`, boardID, cardID, attemptID, claimID)
	if err != nil {
		return workboard.ExecutionAdmissionRecord{}, false, err
	}
	defer rows.Close()
	tasks := []string{}
	for rows.Next() {
		var task string
		if rows.Scan(&task) != nil {
			return workboard.ExecutionAdmissionRecord{}, false, ErrWorkboardCorrupt
		}
		tasks = append(tasks, task)
	}
	if rows.Err() != nil {
		return workboard.ExecutionAdmissionRecord{}, false, rows.Err()
	}
	if len(tasks) == 0 {
		return workboard.ExecutionAdmissionRecord{}, false, nil
	}
	if len(tasks) != 1 {
		return workboard.ExecutionAdmissionRecord{}, true, ErrWorkboardCorrupt
	}
	indexed, canonical, found, err := readExecutionAdmission(ctx, tx, tasks[0])
	if err != nil || !found || indexed != canonical || canonical.BoardID != boardID || canonical.CardID != cardID ||
		canonical.AttemptID != attemptID || canonical.ClaimID != claimID {
		if err != nil {
			return workboard.ExecutionAdmissionRecord{}, true, err
		}
		return workboard.ExecutionAdmissionRecord{}, true, ErrWorkboardCorrupt
	}
	return canonical, true, nil
}

func readExecutionTerminalProof(ctx context.Context, tx *sql.Tx, admission workboard.ExecutionAdmissionRecord) (executionTerminalProof, error) {
	var session, state string
	var head int64
	if err := tx.QueryRowContext(ctx, `SELECT session_id,sequence,state FROM task_heads WHERE task_id=?`, admission.TaskID).Scan(&session, &head, &state); err != nil ||
		session != admission.SessionID || head < 2 {
		return executionTerminalProof{}, ErrWorkboardCorrupt
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.sequence,e.body,l.body_digest FROM events e
		JOIN event_log l ON l.event_id=e.id AND l.task_id=e.task_id AND l.task_sequence=e.sequence
		WHERE e.task_id=? ORDER BY e.sequence`, admission.TaskID)
	if err != nil {
		return executionTerminalProof{}, err
	}
	defer rows.Close()
	var start, terminal runtime.Event
	var terminalDigest string
	turns := map[string]bool{}
	tokens, completed, usageUnknown := int64(0), 0, false
	count := int64(0)
	for rows.Next() {
		var id, digest string
		var sequence int64
		var body []byte
		var event runtime.Event
		if rows.Scan(&id, &sequence, &body, &digest) != nil || json.Unmarshal(body, &event) != nil || event.Validate() != nil ||
			event.ID != id || event.TaskID != admission.TaskID || event.SessionID != admission.SessionID || event.Sequence != sequence ||
			event.CorrelationID != admission.TaskID || event.WorkerID != admission.WorkerID || sequence != count+1 || streamBodyDigest(body) != digest {
			return executionTerminalProof{}, ErrWorkboardCorrupt
		}
		canonical, encodeErr := event.Encode()
		if encodeErr != nil || !bytes.Equal(canonical, body) {
			return executionTerminalProof{}, ErrWorkboardCorrupt
		}
		count++
		if sequence == 1 {
			start = event
		}
		if terminalRuntimeEvent(event.Kind) {
			if sequence != head {
				return executionTerminalProof{}, ErrWorkboardCorrupt
			}
			terminal, terminalDigest = event, digest
		}
		key := event.TurnID + "\x00" + event.AttemptID
		switch event.Kind {
		case runtime.TurnStarted:
			if event.Data.ProviderID != admission.ProviderID || event.Data.ModelID != admission.ModelID || turns[key] {
				return executionTerminalProof{}, ErrWorkboardCorrupt
			}
			turns[key] = true
		case runtime.TurnCompleted:
			if !turns[key] || event.Data.ProviderID != "" && event.Data.ProviderID != admission.ProviderID ||
				event.Data.ModelID != "" && event.Data.ModelID != admission.ModelID {
				return executionTerminalProof{}, ErrWorkboardCorrupt
			}
			delete(turns, key)
			completed++
			if event.Data.Usage == nil {
				usageUnknown = true
			} else {
				input, output := event.Data.Usage.InputTokens, event.Data.Usage.OutputTokens
				if input < 0 || output < 0 || input > math.MaxInt64-output || input+output > workboard.MaxWorkTokens ||
					tokens > workboard.MaxWorkTokens-(input+output) {
					return executionTerminalProof{}, ErrWorkboardCorrupt
				}
				if !usageUnknown {
					tokens += input + output
				}
			}
		}
	}
	if rows.Err() != nil {
		return executionTerminalProof{}, rows.Err()
	}
	wantState := map[runtime.Kind]string{runtime.TaskCompleted: "completed", runtime.TaskFailed: "failed", runtime.TaskCanceled: "canceled"}[terminal.Kind]
	if count != head || start.Kind != runtime.TaskStarted || start.ID != admission.EventID || streamBodyDigestMust(start) != admission.EventDigest ||
		start.Data.ModelID != admission.ModelID || start.Data.ProviderID != admission.ProviderID || terminal.Sequence != head || wantState == "" || state != wantState {
		return executionTerminalProof{}, ErrWorkboardCorrupt
	}
	known := completed > 0 && !usageUnknown && len(turns) == 0
	if !known {
		tokens = admission.TokenLimit
	}
	timeMS, err := executionElapsedMillis(start.Time, terminal.Time, admission.TimeLimitMS)
	if err != nil {
		return executionTerminalProof{}, err
	}
	return executionTerminalProof{event: terminal, digest: terminalDigest, timeMS: timeMS, tokens: tokens, tokensKnown: known}, nil
}

func executionElapsedMillis(start, terminal time.Time, limit int64) (int64, error) {
	if terminal.Before(start) {
		return limit, nil
	}
	delta := terminal.Sub(start)
	millis := int64(delta / time.Millisecond)
	if delta%time.Millisecond != 0 {
		millis++
	}
	if millis > workboard.MaxWorkDurationMillis {
		return 0, ErrWorkboardCorrupt
	}
	return millis, nil
}

func streamBodyDigestMust(event runtime.Event) string {
	body, err := event.Encode()
	if err != nil {
		return ""
	}
	return streamBodyDigest(body)
}

func terminalRuntimeEvent(kind runtime.Kind) bool {
	return kind == runtime.TaskCompleted || kind == runtime.TaskFailed || kind == runtime.TaskCanceled
}

func executionSettlementRecord(admission workboard.ExecutionAdmissionRecord, proof executionTerminalProof, id string, settledAt time.Time) (workboard.ExecutionSettlementRecord, error) {
	if settledAt.Before(proof.event.Time) {
		return workboard.ExecutionSettlementRecord{}, ErrWorkboardCorrupt
	}
	record := workboard.ExecutionSettlementRecord{Version: 1, SettlementID: id, AdmissionID: admission.AdmissionID,
		AdmissionDigest: admission.AdmissionDigest, TaskID: admission.TaskID, SessionID: admission.SessionID, BoardID: admission.BoardID,
		CardID: admission.CardID, AttemptID: admission.AttemptID, ClaimID: admission.ClaimID, WorkerID: admission.WorkerID,
		OperationID: admission.OperationID, ModelID: admission.ModelID, ProviderID: admission.ProviderID, ConfigID: admission.ConfigID,
		TerminalEventID: proof.event.ID, TerminalEventDigest: proof.digest, TerminalKind: proof.event.Kind,
		TerminalSequence: proof.event.Sequence, ChargedTimeMS: proof.timeMS, ChargedTokens: proof.tokens,
		ChargedCostMicros: admission.CostMicros, TokenUsageKnown: proof.tokensKnown, SettledAt: settledAt.UTC()}
	var err error
	record.SettlementDigest, err = record.CanonicalDigest()
	if err != nil || record.Validate() != nil {
		return workboard.ExecutionSettlementRecord{}, ErrWorkboardCorrupt
	}
	return record, nil
}

func executionAttemptFinalizedAt(ctx context.Context, tx *sql.Tx, admission workboard.ExecutionAdmissionRecord) (time.Time, error) {
	var endedAt int64
	var encoded string
	var state, claimState string
	err := tx.QueryRowContext(ctx, `SELECT a.ended_at,json_extract(a.body,'$.ended_at'),a.state,c.state
		FROM workboard_attempts a JOIN workboard_claims c ON c.board_id=a.board_id AND c.card_id=a.card_id AND c.attempt_id=a.id
		WHERE a.board_id=? AND a.card_id=? AND a.id=? AND c.id=?`, admission.BoardID, admission.CardID, admission.AttemptID, admission.ClaimID).
		Scan(&endedAt, &encoded, &state, &claimState)
	if err != nil || state == "running" || claimState != "released" {
		return time.Time{}, ErrWorkboardCorrupt
	}
	// Acceptance and rejection advance the attempt again and replace ended_at
	// with the decision time. The execution reservation was released by the
	// earlier candidate submission, whose canonical candidate creation time is
	// therefore the stable settlement boundary used by exact replay.
	if state == "review" || state == "accepted" || state == "rejected" {
		candidate, candidateErr := readEvaluationCandidate(ctx, tx, admission.BoardID, admission.CardID, admission.AttemptID)
		if candidateErr != nil || candidate.BoardID != admission.BoardID || candidate.CardID != admission.CardID || candidate.AttemptID != admission.AttemptID {
			return time.Time{}, ErrWorkboardCorrupt
		}
		return candidate.CreatedAt.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, encoded)
	if err != nil || parsed.UnixNano() != endedAt {
		return time.Time{}, ErrWorkboardCorrupt
	}
	return parsed.UTC(), nil
}

func readExecutionSettlement(ctx context.Context, tx *sql.Tx, admissionID string) (workboard.ExecutionSettlementRecord, workboard.ExecutionSettlementRecord, bool, error) {
	var indexed workboard.ExecutionSettlementRecord
	var terminalKind string
	var known int
	var settledAt int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT settlement_id,admission_id,admission_digest,task_id,session_id,board_id,card_id,attempt_id,
		claim_id,worker_id,operation_id,model_id,provider_id,config_id,terminal_event_id,terminal_event_digest,terminal_kind,
		terminal_sequence,charged_time_ms,charged_tokens,charged_cost_micros,token_usage_known,settled_at,settlement_digest,body
		FROM workboard_execution_settlements WHERE admission_id=?`, admissionID).Scan(&indexed.SettlementID, &indexed.AdmissionID,
		&indexed.AdmissionDigest, &indexed.TaskID, &indexed.SessionID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID,
		&indexed.ClaimID, &indexed.WorkerID, &indexed.OperationID, &indexed.ModelID, &indexed.ProviderID, &indexed.ConfigID,
		&indexed.TerminalEventID, &indexed.TerminalEventDigest, &terminalKind, &indexed.TerminalSequence, &indexed.ChargedTimeMS,
		&indexed.ChargedTokens, &indexed.ChargedCostMicros, &known, &settledAt, &indexed.SettlementDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.ExecutionSettlementRecord{}, workboard.ExecutionSettlementRecord{}, false, nil
	}
	if err != nil {
		return workboard.ExecutionSettlementRecord{}, workboard.ExecutionSettlementRecord{}, false, err
	}
	indexed.Version, indexed.TerminalKind, indexed.TokenUsageKnown = 1, runtime.Kind(terminalKind), known == 1
	indexed.SettledAt = time.Unix(0, settledAt).UTC()
	var canonical workboard.ExecutionSettlementRecord
	if known != 0 && known != 1 || strictJSON(body, &canonical) != nil || indexed.Validate() != nil || canonical.Validate() != nil {
		return indexed, canonical, true, ErrWorkboardCorrupt
	}
	return indexed, canonical, true, nil
}
