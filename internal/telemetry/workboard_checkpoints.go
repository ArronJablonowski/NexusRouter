package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func appendStoredCheckpoint(ctx context.Context, tx *sql.Tx, mutation workboard.ProgressMutation, card workboard.Card, body storedWorkboardCard) (int64, int, error) {
	if card.Revision != mutation.ExpectedCardRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.State != workboard.InProgress && card.State != workboard.Blocked || card.CurrentAttemptID != mutation.AttemptID ||
		card.CurrentClaimID != mutation.ClaimID || card.CriteriaRevision != mutation.CriteriaRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "checkpoint"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, mutation.BoardID, mutation.CardID, mutation.AttemptID, mutation.ClaimID)
	if err != nil {
		return 0, 0, err
	}
	if lease.Revision != mutation.ExpectedClaimRevision {
		return 0, 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	if lease.OwnerID != mutation.Actor.ID {
		return 0, 0, &workboard.Violation{Code: workboard.CodeLeaseOwner, Field: "owner"}
	}
	if lease.State != workboard.LeaseActive || !mutation.Now.Before(lease.ExpiresAt) {
		return 0, 0, &workboard.Violation{Code: workboard.CodeLeaseExpired, Field: "claim"}
	}
	attempt, attemptBytes, err := readCheckpointAttempt(ctx, tx, mutation, claim)
	if err != nil {
		return 0, 0, err
	}
	if attempt.CriteriaRevision != mutation.CriteriaRevision || attempt.CriteriaRevision != body.CriteriaRevision ||
		attempt.CriteriaDigest != criteriaDigest(card.Criteria) {
		return 0, 0, ErrWorkboardCorrupt
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0)+1 FROM workboard_checkpoints WHERE board_id=? AND card_id=? AND attempt_id=?`,
		mutation.BoardID, mutation.CardID, mutation.AttemptID).Scan(&revision); err != nil {
		return 0, 0, err
	}
	checkpoint := workboard.CheckpointRecord{Version: 1, ID: newWorkboardID(), BoardID: mutation.BoardID, CardID: mutation.CardID,
		AttemptID: mutation.AttemptID, ClaimID: mutation.ClaimID, Revision: revision, ClaimRevision: lease.Revision,
		CriteriaRevision: attempt.CriteriaRevision, CriteriaDigest: attempt.CriteriaDigest, PolicyDigest: attempt.PolicyDigest,
		Evidence: mutation.Evidence, EvidenceDigest: digestBytes([]byte(mutation.Evidence)), ActorID: mutation.Actor.ID,
		ActorType: mutation.Actor.Type, CreatedAt: mutation.Now}
	if checkpoint.ID == "" {
		return 0, 0, errors.New("secure identifier generation failed")
	}
	if checkpoint.Validate() != nil {
		return 0, 0, ErrWorkboardCorrupt
	}
	checkpointBytes, err := encodeLifecycle(checkpoint, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_checkpoints(id,board_id,card_id,attempt_id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,
		policy_digest,evidence,evidence_digest,actor_id,actor_type,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		checkpoint.ID, checkpoint.BoardID, checkpoint.CardID, checkpoint.AttemptID, checkpoint.ClaimID, checkpoint.Revision,
		checkpoint.ClaimRevision, checkpoint.CriteriaRevision, checkpoint.CriteriaDigest, checkpoint.PolicyDigest, checkpoint.Evidence,
		checkpoint.EvidenceDigest, checkpoint.ActorID, checkpoint.ActorType, checkpoint.CreatedAt.UnixNano(), checkpointBytes); err != nil {
		return 0, 0, normalizeProgressWriteError(err)
	}
	return lease.Revision, len(attemptBytes) + len(checkpointBytes), nil
}

func readCheckpointAttempt(ctx context.Context, tx *sql.Tx, mutation workboard.ProgressMutation, claim storedLifecycleClaim) (storedLifecycleAttempt, []byte, error) {
	var attempt storedLifecycleAttempt
	var revision int64
	var state, workerID, criteriaDigestValue, policyDigest string
	var endedAt sql.NullInt64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts
		WHERE board_id=? AND card_id=? AND id=?`, mutation.BoardID, mutation.CardID, mutation.AttemptID).
		Scan(&revision, &state, &workerID, &criteriaDigestValue, &policyDigest, &endedAt, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardNotFound
	}
	if err != nil {
		return storedLifecycleAttempt{}, nil, err
	}
	if strictJSON(body, &attempt) != nil || attempt.Revision != revision || attempt.State != state || attempt.WorkerID != workerID ||
		attempt.CriteriaDigest != criteriaDigestValue || attempt.PolicyDigest != policyDigest || endedAt.Valid || attempt.State != "running" ||
		attempt.ID != mutation.AttemptID || attempt.BoardID != mutation.BoardID || attempt.CardID != mutation.CardID ||
		attempt.WorkerID != mutation.Actor.ID || !equalStoredClaim(attempt.Claim, claim) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardCorrupt
	}
	return attempt, body, nil
}

func validateProgressMutation(m workboard.ProgressMutation) error {
	if m.Version != workboard.ProgressMutationVersion || !validWorkboardID(m.BoardID) || !validWorkboardID(m.CardID) ||
		m.Actor.Validate() != nil || len(m.IdempotencyKey) < 16 || len(m.IdempotencyKey) > 128 || !validDigest(m.RequestDigest) ||
		m.Now.Location() != time.UTC || m.Now.Year() < 1970 || m.Now.Year() >= 2261 {
		return invalidWorkboard("mutation")
	}
	for _, r := range m.IdempotencyKey {
		if r < 0x21 || r > 0x7e {
			return invalidWorkboard("mutation")
		}
	}
	switch m.Kind {
	case workboard.ProgressCriteriaRevise:
		if m.Actor.Type != "operator" || m.AttemptID != "" || m.ClaimID != "" || m.ExpectedCardRevision < 1 ||
			m.ExpectedClaimRevision != 0 || m.ExpectedCriteriaRevision < 1 || m.CriteriaRevision != 0 ||
			workboard.ValidateCriteriaSet(m.Criteria, m.ExpectedCriteriaRevision+1) != nil || m.Evidence != "" {
			return invalidWorkboard("criteria")
		}
	case workboard.ProgressCheckpointAppend:
		if m.Actor.Type != "worker" || !validWorkboardID(m.AttemptID) || !validWorkboardID(m.ClaimID) || m.ExpectedCardRevision < 1 ||
			m.ExpectedClaimRevision < 1 || m.ExpectedCriteriaRevision != 0 || m.CriteriaRevision < 1 || m.Criteria != nil ||
			workboard.ValidateCheckpointEvidence(m.Evidence) != nil {
			return invalidWorkboard("checkpoint")
		}
	default:
		return invalidWorkboard("kind")
	}
	return nil
}

func progressAction(kind workboard.ProgressKind) workboard.BoardAction {
	switch kind {
	case workboard.ProgressCriteriaRevise:
		return workboard.CriteriaReviseAction
	case workboard.ProgressCheckpointAppend:
		return workboard.CheckpointAppendAction
	default:
		return ""
	}
}

func normalizeProgressWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "workboard_checkpoint_limit") {
		return &workboard.Violation{Code: workboard.CodeLimitExceeded, Field: "checkpoints"}
	}
	if strings.Contains(message, "unique") {
		return ErrConflict
	}
	return err
}
