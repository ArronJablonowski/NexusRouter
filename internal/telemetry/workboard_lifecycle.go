package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func (s *Store) ApplyLifecycleMutation(ctx context.Context, mutation workboard.LifecycleMutation) (workboard.OperationReceipt, error) {
	if err := validateLifecycleMutation(mutation, true); err != nil {
		return workboard.OperationReceipt{}, err
	}
	requestDigest, err := workboard.LifecycleDigest(mutation)
	if err != nil || requestDigest != mutation.RequestDigest {
		return workboard.OperationReceipt{}, invalidWorkboard("request_digest")
	}
	keyDigest := digestBytes([]byte(mutation.IdempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if receipt, found, replayErr := readLifecycleReplay(ctx, tx, mutation, keyDigest, requestDigest); found || replayErr != nil {
		return receipt, replayErr
	}
	board, _, _, err := readBoardRow(ctx, tx, mutation.BoardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if board.State != "active" {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "board_state"}
	}
	card, cardBody, err := readStoredCard(ctx, tx, mutation.BoardID, mutation.CardID)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	var claimRevision int64
	var durableBytes int
	switch mutation.Kind {
	case workboard.LifecycleClaim:
		claimRevision, durableBytes, err = applyClaim(ctx, tx, mutation, &board, &card, &cardBody)
	case workboard.LifecycleHeartbeat:
		claimRevision, durableBytes, err = applyClaimHeartbeat(ctx, tx, mutation, &board, card)
	case workboard.LifecycleRecover:
		claimRevision, durableBytes, err = applyClaimRecovery(ctx, tx, mutation, &board, &card, &cardBody)
	case workboard.LifecycleFail:
		claimRevision, durableBytes, err = applyClaimFailure(ctx, tx, mutation, &board, &card, &cardBody)
	}
	if err != nil {
		return workboard.OperationReceipt{}, fmt.Errorf("apply lifecycle projection: %w", err)
	}
	operationID, eventID := newWorkboardID(), newWorkboardID()
	if operationID == "" || eventID == "" {
		return workboard.OperationReceipt{}, errors.New("secure identifier generation failed")
	}
	board.Revision++
	board.EventSequence++
	board.UpdatedAt = mutation.Now
	boardBody, err := json.Marshal(board)
	if err != nil || board.Validate() != nil {
		return workboard.OperationReceipt{}, fmt.Errorf("encode lifecycle board: %w", ErrWorkboardCorrupt)
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence, OperationID: operationID,
		Kind: lifecycleAction(mutation.Kind), ActorID: mutation.Actor.ID, ActorType: mutation.Actor.Type, CardID: card.ID, CreatedAt: mutation.Now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil {
		return workboard.OperationReceipt{}, fmt.Errorf("encode lifecycle event: %w", ErrWorkboardCorrupt)
	}
	cardRevision := card.Revision
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: requestDigest,
		FirstSequence: board.EventSequence, LastSequence: board.EventSequence, EventCount: 1, BoardRevision: board.Revision,
		CardID: card.ID, CardRevision: &cardRevision, ClaimRevision: &claimRevision, Outcome: "committed", CreatedAt: mutation.Now}
	response, err := finalizeWorkboardReceipt(&receipt, durableBytes+len(boardBody)+len(eventBody))
	if err != nil {
		return workboard.OperationReceipt{}, fmt.Errorf("finalize lifecycle receipt: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_boards SET revision=?,layout_revision=?,event_sequence=?,active_claims=?,updated_at=?,body=? WHERE id=? AND revision=?`,
		board.Revision, board.LayoutRevision, board.EventSequence, board.ActiveClaims, board.UpdatedAt.UnixNano(), boardBody, board.ID, board.Revision-1)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return workboard.OperationReceipt{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "board_revision"}
	}
	if err = insertWorkboardOperation(ctx, tx, "board", board.ID, keyDigest, receipt, response); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = insertWorkboardEvent(ctx, tx, eventID, board.ID, board.EventSequence, operationID, string(event.Kind), card.ID, mutation.Actor, mutation.Now, eventBody); err != nil {
		return workboard.OperationReceipt{}, fmt.Errorf("insert lifecycle event: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func applyClaim(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, board *workboard.Board, card *workboard.Card, body *storedWorkboardCard) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.State != workboard.Ready || card.RemainingDependencies != 0 || card.CurrentClaimID != "" || body.AttemptCount >= body.Budget.AttemptLimit ||
		body.AssigneeID != "" && body.AssigneeID != mutation.Actor.ID {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "claim"}
	}
	if mutation.TaskID != "" {
		var sessionID, state string
		if err := tx.QueryRowContext(ctx, `SELECT session_id,state FROM task_heads WHERE task_id=?`, mutation.TaskID).Scan(&sessionID, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "runtime_binding"}
			}
			return 0, 0, err
		}
		var priorBindings int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_attempt_tasks WHERE task_id=?`, mutation.TaskID).Scan(&priorBindings); err != nil {
			return 0, 0, err
		}
		if sessionID != mutation.SessionID || state != "running" || priorBindings != 0 {
			return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "runtime_binding"}
		}
	}
	predecessorAttemptID := body.CurrentAttemptID
	attemptID, claimID := newWorkboardID(), newWorkboardID()
	if attemptID == "" || claimID == "" {
		return 0, 0, errors.New("secure identifier generation failed")
	}
	criteriaDigest, err := digestJSON(body.Criteria)
	if err != nil {
		return 0, 0, err
	}
	lease := workboard.Lease{BoardID: board.ID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID, Revision: 1,
		State: workboard.LeaseActive, OwnerID: mutation.Actor.ID, LastHeartbeat: mutation.Now, ExpiresAt: mutation.Now.Add(mutation.LeaseTTL).UTC()}
	if workboard.ValidateLease(lease) != nil {
		return 0, 0, invalidWorkboard("lease")
	}
	claim := storedLifecycleClaim{Version: 1, ID: claimID, BoardID: board.ID, CardID: card.ID, AttemptID: attemptID, Revision: 1,
		State: string(lease.State), OwnerID: mutation.Actor.ID, OwnerType: "worker", TaskID: mutation.TaskID,
		ExpiresAt: lease.ExpiresAt, LastHeartbeat: lease.LastHeartbeat}
	taskIDs, sessionIDs := []string{}, []string{}
	if mutation.TaskID != "" {
		taskIDs, sessionIDs = []string{mutation.TaskID}, []string{mutation.SessionID}
	}
	body.AttemptCount++
	attempt := storedLifecycleAttempt{Version: 1, ID: attemptID, BoardID: board.ID, CardID: card.ID, Ordinal: body.AttemptCount, Revision: 1,
		State: "running", WorkerID: mutation.Actor.ID, CriteriaRevision: body.CriteriaRevision, CriteriaDigest: criteriaDigest,
		PolicyDigest: mutation.PolicyDigest, Budget: body.Budget, Criteria: append([]storedWorkboardCriterion{}, body.Criteria...),
		TaskIDs: taskIDs, SessionIDs: sessionIDs, Claim: claim, StartedAt: mutation.Now}
	attemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, 0, err
	}
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	heartbeat := storedClaimHeartbeat{Version: 1, ClaimID: claimID, Revision: 1, ObservedAt: mutation.Now, ExpiresAt: lease.ExpiresAt, ActorID: mutation.Actor.ID}
	heartbeatBytes, err := encodeLifecycle(heartbeat, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_attempts(id,board_id,card_id,ordinal,revision,state,worker_id,criteria_revision,criteria_digest,policy_digest,
		attempt_limit,time_limit_ms,token_limit,cost_micros,started_at,ended_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
		attempt.ID, attempt.BoardID, attempt.CardID, attempt.Ordinal, attempt.Revision, attempt.State, attempt.WorkerID, attempt.CriteriaRevision,
		attempt.CriteriaDigest, attempt.PolicyDigest, attempt.Budget.AttemptLimit, attempt.Budget.TimeLimitMS, attempt.Budget.TokenLimit,
		attempt.Budget.CostMicros, mutation.Now.UnixNano(), attemptBytes); err != nil {
		return 0, 0, normalizeLifecycleWriteError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_claims(id,board_id,card_id,attempt_id,revision,state,owner_id,owner_type,task_id,expires_at,last_heartbeat,released_at,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,NULL,?)`, claim.ID, claim.BoardID, claim.CardID, claim.AttemptID, claim.Revision, claim.State,
		claim.OwnerID, claim.OwnerType, nullable(claim.TaskID), claim.ExpiresAt.UnixNano(), claim.LastHeartbeat.UnixNano(), claimBytes); err != nil {
		return 0, 0, normalizeLifecycleWriteError(err)
	}
	if mutation.TaskID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_attempt_tasks(board_id,card_id,attempt_id,ordinal,task_id) VALUES(?,?,?,?,?)`,
			board.ID, card.ID, attemptID, 0, mutation.TaskID); err != nil {
			return 0, 0, normalizeLifecycleWriteError(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_attempt_sessions(board_id,card_id,attempt_id,ordinal,session_id) VALUES(?,?,?,?,?)`,
			board.ID, card.ID, attemptID, 0, mutation.SessionID); err != nil {
			return 0, 0, normalizeLifecycleWriteError(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_claim_heartbeats(claim_id,revision,observed_at,expires_at,actor_id,body) VALUES(?,?,?,?,?,?)`,
		claim.ID, 1, mutation.Now.UnixNano(), claim.ExpiresAt.UnixNano(), mutation.Actor.ID, heartbeatBytes); err != nil {
		return 0, 0, err
	}
	reassignmentBytes, err := insertDerivedReassignment(ctx, tx, mutation, predecessorAttemptID, attemptID, claimID)
	if err != nil {
		return 0, 0, err
	}
	body.CurrentAttemptID, body.CurrentClaimID, body.AssigneeID, body.State = attemptID, claimID, mutation.Actor.ID, string(workboard.InProgress)
	card.CurrentAttemptID, card.CurrentClaimID, card.AssigneeID, card.State = attemptID, claimID, mutation.Actor.ID, workboard.InProgress
	inProgressRank, err := appendRank(ctx, tx, board.ID, workboard.InProgress)
	if err != nil {
		return 0, 0, err
	}
	card.Rank, body.Rank = inProgressRank, inProgressRank
	card.AttemptCount = body.AttemptCount
	card.Revision++
	card.UpdatedAt = mutation.Now
	board.ActiveClaims++
	board.LayoutRevision++
	cardBytes, err := writeLifecycleCard(ctx, tx, *card, *body, mutation.ExpectedCardRevision)
	if err != nil {
		return 0, 0, fmt.Errorf("write claimed card: %w", err)
	}
	return 1, len(attemptBytes) + len(claimBytes) + len(heartbeatBytes) + reassignmentBytes + cardBytes, err
}

func applyClaimHeartbeat(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, board *workboard.Board, card workboard.Card) (int64, int, error) {
	if card.CurrentClaimID != mutation.ClaimID || card.State != workboard.InProgress && card.State != workboard.Blocked {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "claim"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	oldClaim := claim
	next, err := workboard.ApplyHeartbeat(lease, workboard.Heartbeat{OwnerID: mutation.Actor.ID, ExpectedRevision: mutation.ExpectedClaimRevision, Now: mutation.Now, TTL: mutation.LeaseTTL})
	if err != nil {
		return 0, 0, err
	}
	claim = updateStoredClaim(claim, next)
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	heartbeat := storedClaimHeartbeat{Version: 1, ClaimID: claim.ID, Revision: claim.Revision, ObservedAt: mutation.Now, ExpiresAt: claim.ExpiresAt, ActorID: mutation.Actor.ID}
	heartbeatBytes, err := encodeLifecycle(heartbeat, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_claims SET revision=?,state=?,expires_at=?,last_heartbeat=?,body=? WHERE id=? AND revision=? AND owner_id=?`,
		claim.Revision, claim.State, claim.ExpiresAt.UnixNano(), claim.LastHeartbeat.UnixNano(), claimBytes, claim.ID, mutation.ExpectedClaimRevision, mutation.Actor.ID)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_claim_heartbeats(claim_id,revision,observed_at,expires_at,actor_id,body) VALUES(?,?,?,?,?,?)`,
		claim.ID, claim.Revision, mutation.Now.UnixNano(), claim.ExpiresAt.UnixNano(), mutation.Actor.ID, heartbeatBytes); err != nil {
		return 0, 0, err
	}
	attemptBytes, err := updateAttemptClaim(ctx, tx, mutation, oldClaim, claim)
	return claim.Revision, len(claimBytes) + len(heartbeatBytes) + attemptBytes, err
}
