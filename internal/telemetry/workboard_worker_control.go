package telemetry

import (
	"context"
	"database/sql"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// ReadWorkerControl returns the control flags only when every supplied fence
// still names the same canonical running attempt and active/attention claim.
// Card, attempt, and claim are read in one SQLite snapshot.
func (s *Store) ReadWorkerControl(ctx context.Context, target workboard.WorkerControlTarget) (workboard.WorkerControlObservation, error) {
	zero := workboard.WorkerControlObservation{}
	if s == nil || ctx == nil || ctx.Err() != nil || target.Validate() != nil {
		return zero, invalidWorkboard("worker_control")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	card, _, err := readStoredCard(ctx, tx, target.BoardID, target.CardID)
	if err != nil {
		return zero, err
	}
	attempt, _, err := readCanonicalAttemptSnapshot(ctx, tx, target.BoardID, target.CardID, target.AttemptID)
	if err != nil {
		return zero, err
	}
	claim := attempt.Claim
	if card.Revision != target.CardRevision || card.CurrentAttemptID != target.AttemptID || card.CurrentClaimID != target.ClaimID ||
		(card.State != workboard.InProgress && card.State != workboard.Blocked) || attempt.State != "running" ||
		attempt.WorkerID != target.WorkerID || claim == nil || claim.ID != target.ClaimID || claim.Revision != target.ClaimRevision ||
		(claim.State != string(workboard.LeaseActive) && claim.State != string(workboard.LeaseAttention)) ||
		claim.OwnerID != target.WorkerID || claim.OwnerType != "worker" || claim.TaskID != target.TaskID {
		return zero, ErrConflict
	}
	result := workboard.WorkerControlObservation{Version: workboard.WorkerControlObservationVersion,
		BoardID: target.BoardID, CardID: target.CardID, AttemptID: target.AttemptID, ClaimID: target.ClaimID,
		WorkerID: target.WorkerID, TaskID: target.TaskID, CardRevision: card.Revision, ClaimRevision: claim.Revision,
		CancelRequested: card.CancelRequested, PauseRequested: card.PauseRequested, PausePhase: card.PausePhase}
	if result.Validate(target) != nil {
		return zero, ErrWorkboardCorrupt
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
