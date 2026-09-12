package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// applyClaimFailure is intentionally narrower than recovery: the current
// worker may release its own claim only after the host has established that
// the failed callback was effect-free. It consumes no recovery proof and
// cannot be used for confirmed or uncertain effects.
func applyClaimFailure(ctx context.Context, tx *sql.Tx, mutation workboard.LifecycleMutation, board *workboard.Board,
	card *workboard.Card, body *storedWorkboardCard,
) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.CurrentAttemptID != mutation.AttemptID || card.CurrentClaimID != mutation.ClaimID ||
		(card.State != workboard.InProgress && card.State != workboard.Blocked) {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "claim"}
	}
	canonical, _, err := readCanonicalAttemptSnapshot(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID)
	if err != nil || canonical.State != "running" || canonical.WorkerID != mutation.Actor.ID || canonical.Claim == nil ||
		canonical.Claim.ID != mutation.ClaimID || canonical.Claim.OwnerID != mutation.Actor.ID {
		return 0, 0, ErrWorkboardCorrupt
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	next, err := workboard.ApplyWorkerFailure(lease, workboard.WorkerFailure{OwnerID: mutation.Actor.ID,
		ExpectedRevision: mutation.ExpectedClaimRevision, Now: mutation.Now, EffectResolution: mutation.EffectResolution})
	if err != nil {
		return 0, 0, err
	}
	claim = updateStoredClaim(claim, next)
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, err
	}
	var attempt storedLifecycleAttempt
	var attemptBytes []byte
	if err = tx.QueryRowContext(ctx, `SELECT body FROM workboard_attempts WHERE board_id=? AND card_id=? AND id=?`,
		mutation.BoardID, mutation.CardID, mutation.AttemptID).Scan(&attemptBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrWorkboardNotFound
		}
		return 0, 0, err
	}
	if strictJSON(attemptBytes, &attempt) != nil || attempt.Revision != canonical.Revision || attempt.State != "running" ||
		!equalStoredClaim(attempt.Claim, updateStoredClaim(claim, lease)) {
		return 0, 0, ErrWorkboardCorrupt
	}
	attempt.Revision++
	attempt.State = "failed"
	attempt.Claim = claim
	attempt.EndedAt = &mutation.Now
	nextAttemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, 0, err
	}
	claimUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_claims SET revision=?,state='released',released_at=?,body=?
		WHERE id=? AND revision=? AND owner_id=? AND state IN ('active','attention')`, claim.Revision, mutation.Now.UnixNano(), claimBytes,
		claim.ID, mutation.ExpectedClaimRevision, mutation.Actor.ID)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := claimUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	attemptUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,state='failed',ended_at=?,body=?
		WHERE id=? AND revision=? AND state='running'`, attempt.Revision, mutation.Now.UnixNano(), nextAttemptBytes, attempt.ID, attempt.Revision-1)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := attemptUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	readyRank, err := appendRank(ctx, tx, board.ID, workboard.Ready)
	if err != nil {
		return 0, 0, err
	}
	card.State, card.Rank, card.AssigneeID, card.CurrentClaimID = workboard.Ready, readyRank, "", ""
	card.BlockReason, card.CancelRequested, card.PauseRequested, card.PausePhase = "", false, false, workboard.PauseNone
	card.Revision++
	card.UpdatedAt = mutation.Now
	*body = updateStoredBody(*body, *card)
	cardBytes, err := json.Marshal(body)
	if err != nil || card.Validate() != nil || !validStoredCardReferences(*body) {
		return 0, 0, ErrWorkboardCorrupt
	}
	cardUpdate, err := tx.ExecContext(ctx, `UPDATE workboard_cards SET revision=?,state=?,rank=?,assignee_id=NULL,block_reason=NULL,
		current_claim_id=NULL,cancel_requested=0,pause_requested=0,updated_at=?,body=? WHERE board_id=? AND id=? AND revision=?`,
		card.Revision, card.State, card.Rank, card.UpdatedAt.UnixNano(), cardBytes, card.BoardID, card.ID, mutation.ExpectedCardRevision)
	if err != nil {
		return 0, 0, err
	}
	if changed, _ := cardUpdate.RowsAffected(); changed != 1 {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	board.ActiveClaims--
	board.LayoutRevision++
	if board.ActiveClaims < 0 {
		return 0, 0, ErrWorkboardCorrupt
	}
	settlementBytes, err := settleExecutionFailureAttempt(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID, mutation.Now)
	if err != nil {
		return 0, 0, err
	}
	return claim.Revision, len(claimBytes) + len(attemptBytes) + len(nextAttemptBytes) + len(cardBytes) + settlementBytes, nil
}
