package workboard

import "time"

// ReassignmentRecord is the immutable link from one proof-gated recovery to
// the next attempt that claimed the card. The successor receives independent
// runtime identity; this record grants no replay or execution authority.
type ReassignmentRecord struct {
	Version              int       `json:"version"`
	RecoveryID           string    `json:"recovery_id"`
	BoardID              string    `json:"board_id"`
	CardID               string    `json:"card_id"`
	PredecessorAttemptID string    `json:"predecessor_attempt_id"`
	PredecessorClaimID   string    `json:"predecessor_claim_id"`
	SuccessorAttemptID   string    `json:"successor_attempt_id"`
	SuccessorClaimID     string    `json:"successor_claim_id"`
	CreatedAt            time.Time `json:"created_at"`
}

func (r ReassignmentRecord) Validate() error {
	if r.Version != SchemaVersion || !validLifecycleIDs(r.RecoveryID, r.BoardID, r.CardID,
		r.PredecessorAttemptID, r.PredecessorClaimID, r.SuccessorAttemptID, r.SuccessorClaimID) ||
		r.PredecessorAttemptID == r.SuccessorAttemptID || r.PredecessorClaimID == r.SuccessorClaimID || !validTime(r.CreatedAt) {
		return fail(CodeInvalid, "reassignment_record")
	}
	return nil
}
