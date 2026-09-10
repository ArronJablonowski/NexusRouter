package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// ReadCardLifecycleSnapshots returns at most one (the latest) attempt per card
// and at most MaxSnapshotCheckpoints recent checkpoints for that attempt.
// Candidate, evidence and acceptance rows are included when those durable
// records exist; no state is synthesized from events or private process data.
func (s *Store) ReadCardLifecycleSnapshots(ctx context.Context, boardID string, cardIDs []string) (map[string]workboard.CardLifecycleSnapshot, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !validWorkboardID(boardID) || len(cardIDs) > workboard.MaxPageItems {
		return nil, invalidWorkboard("lifecycle_snapshot")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := make(map[string]workboard.CardLifecycleSnapshot, len(cardIDs))
	seen := make(map[string]bool, len(cardIDs))
	for _, cardID := range cardIDs {
		if !validWorkboardID(cardID) || seen[cardID] {
			return nil, invalidWorkboard("card_id")
		}
		seen[cardID] = true
		snapshot, found, readErr := readCardLifecycleSnapshot(ctx, tx, boardID, cardID)
		if readErr != nil {
			return nil, readErr
		}
		if found {
			result[cardID] = snapshot
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func readCardLifecycleSnapshot(ctx context.Context, tx *sql.Tx, boardID, cardID string) (workboard.CardLifecycleSnapshot, bool, error) {
	var attemptID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM workboard_attempts
		WHERE board_id=? AND card_id=? ORDER BY ordinal DESC LIMIT 1`, boardID, cardID).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.CardLifecycleSnapshot{}, false, nil
	}
	if err != nil {
		return workboard.CardLifecycleSnapshot{}, false, err
	}
	attempt, count, err := readCanonicalAttemptSnapshot(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.CardLifecycleSnapshot{}, false, err
	}
	checkpoints, _, err := readSnapshotCheckpoints(ctx, tx, boardID, cardID, attemptID)
	if err != nil {
		return workboard.CardLifecycleSnapshot{}, false, err
	}
	return workboard.CardLifecycleSnapshot{CardID: cardID, Attempt: &attempt, Checkpoints: checkpoints,
		CheckpointCount: count, CheckpointsHasMore: count > len(checkpoints)}, true, nil
}

func attemptSnapshot(stored storedLifecycleAttempt) workboard.AttemptSnapshot {
	claim := &workboard.ClaimSnapshot{ID: stored.Claim.ID, BoardID: stored.Claim.BoardID, CardID: stored.Claim.CardID,
		AttemptID: stored.Claim.AttemptID, Revision: stored.Claim.Revision, State: stored.Claim.State, OwnerID: stored.Claim.OwnerID,
		OwnerType: stored.Claim.OwnerType, TaskID: stored.Claim.TaskID, ExpiresAt: stored.Claim.ExpiresAt,
		LastHeartbeat: stored.Claim.LastHeartbeat, ReleasedAt: stored.Claim.ReleasedAt}
	return workboard.AttemptSnapshot{ID: stored.ID, BoardID: stored.BoardID, CardID: stored.CardID, Ordinal: stored.Ordinal,
		Revision: stored.Revision, State: stored.State, WorkerID: stored.WorkerID, CriteriaRevision: stored.CriteriaRevision,
		CriteriaDigest: stored.CriteriaDigest, PolicyDigest: stored.PolicyDigest, Budget: domainWorkboardBudget(stored.Budget),
		Criteria: domainWorkboardCriteria(stored.Criteria), TaskIDs: append([]string{}, stored.TaskIDs...),
		SessionIDs: append([]string{}, stored.SessionIDs...), Claim: claim, Evidence: []workboard.EvidenceRecord{},
		StartedAt: stored.StartedAt, EndedAt: stored.EndedAt}
}

func readSnapshotCandidate(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.CandidateRecord, bool, error) {
	var body []byte
	var indexed workboard.CandidateRecord
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,revision,digest,criteria_digest,policy_digest,evidence_digest,evidence_count,submitted_by,created_at,body
		FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).
		Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.Revision, &indexed.Digest, &indexed.CriteriaDigest,
			&indexed.PolicyDigest, &indexed.EvidenceDigest, &indexed.EvidenceCount, &indexed.SubmittedBy, &created, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.CandidateRecord{}, false, nil
	}
	var candidate workboard.CandidateRecord
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, created).UTC()
	if err != nil || strictJSON(body, &candidate) != nil || candidate.Validate() != nil || candidate.BoardID != boardID || candidate.CardID != cardID || candidate.AttemptID != attemptID ||
		candidate.ID != indexed.ID || candidate.Revision != indexed.Revision || candidate.Digest != indexed.Digest || candidate.CriteriaDigest != indexed.CriteriaDigest ||
		candidate.PolicyDigest != indexed.PolicyDigest || candidate.EvidenceDigest != indexed.EvidenceDigest || candidate.EvidenceCount != indexed.EvidenceCount ||
		candidate.SubmittedBy != indexed.SubmittedBy || !candidate.CreatedAt.Equal(indexed.CreatedAt) {
		if err != nil {
			return workboard.CandidateRecord{}, false, err
		}
		return workboard.CandidateRecord{}, false, ErrWorkboardCorrupt
	}
	artifacts, artifactErr := readSnapshotArtifacts(ctx, tx, candidate)
	if artifactErr != nil || !reflect.DeepEqual(artifacts, candidate.ArtifactRefs) {
		return workboard.CandidateRecord{}, false, ErrWorkboardCorrupt
	}
	return candidate, true, nil
}

func readSnapshotArtifacts(ctx context.Context, tx *sql.Tx, candidate workboard.CandidateRecord) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_ref FROM workboard_candidate_artifacts
		WHERE board_id=? AND card_id=? AND attempt_id=? AND candidate_id=? ORDER BY ordinal`, candidate.BoardID, candidate.CardID, candidate.AttemptID, candidate.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if rows.Scan(&value) != nil || len(result) >= workboard.MaxCandidateArtifacts {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readSnapshotEvidence(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) ([]workboard.EvidenceRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,revision,candidate_id,criterion_id,source,outcome,actor_id,actor_type,reference,candidate_digest,
		criteria_digest,policy_digest,created_at,body FROM workboard_evidence WHERE board_id=? AND card_id=? AND attempt_id=? ORDER BY revision`, boardID, cardID, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workboard.EvidenceRecord{}
	for rows.Next() {
		var body []byte
		var evidence, indexed workboard.EvidenceRecord
		var created int64
		if rows.Scan(&indexed.ID, &indexed.Revision, &indexed.CandidateID, &indexed.CriterionID, &indexed.Source, &indexed.Outcome,
			&indexed.ActorID, &indexed.ActorType, &indexed.Reference, &indexed.CandidateDigest, &indexed.CriteriaDigest, &indexed.PolicyDigest, &created, &body) != nil {
			return nil, ErrWorkboardCorrupt
		}
		indexed.Version, indexed.BoardID, indexed.CardID, indexed.AttemptID, indexed.CreatedAt = 1, boardID, cardID, attemptID, time.Unix(0, created).UTC()
		if strictJSON(body, &evidence) != nil || evidence.Validate() != nil || !reflect.DeepEqual(evidence, indexed) || len(result) >= workboard.MaxEvaluationEvidence {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, evidence)
	}
	return result, rows.Err()
}

func readSnapshotAcceptance(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.AcceptanceRecord, bool, error) {
	var body []byte
	var indexed workboard.AcceptanceRecord
	var decided int64
	err := tx.QueryRowContext(ctx, `SELECT id,candidate_id,candidate_digest,criteria_revision,criteria_digest,evidence_head_revision,evidence_set_digest,
		policy_digest,decision,decided_by,decided_by_type,decision_authority_id,decided_at,body FROM workboard_acceptances
		WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).
		Scan(&indexed.ID, &indexed.CandidateID, &indexed.CandidateDigest, &indexed.CriteriaRevision, &indexed.CriteriaDigest,
			&indexed.EvidenceHeadRevision, &indexed.EvidenceSetDigest, &indexed.PolicyDigest, &indexed.Decision, &indexed.DecidedBy,
			&indexed.DecidedByType, &indexed.DecisionAuthorityID, &decided, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AcceptanceRecord{}, false, nil
	}
	var acceptance workboard.AcceptanceRecord
	indexed.Version, indexed.BoardID, indexed.CardID, indexed.AttemptID, indexed.DecidedAt = 1, boardID, cardID, attemptID, time.Unix(0, decided).UTC()
	if err != nil || strictJSON(body, &acceptance) != nil || acceptance.Validate() != nil || acceptance.BoardID != boardID || acceptance.CardID != cardID || acceptance.AttemptID != attemptID {
		if err != nil {
			return workboard.AcceptanceRecord{}, false, err
		}
		return workboard.AcceptanceRecord{}, false, ErrWorkboardCorrupt
	}
	// These fields are intentionally body-only. Copying them into the indexed
	// projection makes DeepEqual check every normalized column without treating
	// the canonical rationale and pre-decision evidence fence as absent.
	indexed.PriorEvidenceHeadRevision = acceptance.PriorEvidenceHeadRevision
	indexed.PriorEvidenceSetDigest = acceptance.PriorEvidenceSetDigest
	indexed.Rationale = acceptance.Rationale
	if !reflect.DeepEqual(acceptance, indexed) {
		return workboard.AcceptanceRecord{}, false, ErrWorkboardCorrupt
	}
	return acceptance, true, nil
}

func readSnapshotCheckpoints(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) ([]workboard.CheckpointRecord, int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workboard_checkpoints WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,claim_id,revision,claim_revision,criteria_revision,criteria_digest,policy_digest,evidence,evidence_digest,
		actor_id,actor_type,created_at,body FROM workboard_checkpoints WHERE board_id=? AND card_id=? AND attempt_id=? ORDER BY revision DESC LIMIT ?`,
		boardID, cardID, attemptID, workboard.MaxSnapshotCheckpoints)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	reversed := make([]workboard.CheckpointRecord, 0, min(count, workboard.MaxSnapshotCheckpoints))
	for rows.Next() {
		var body []byte
		var checkpoint, indexed workboard.CheckpointRecord
		var created int64
		if rows.Scan(&indexed.ID, &indexed.ClaimID, &indexed.Revision, &indexed.ClaimRevision, &indexed.CriteriaRevision,
			&indexed.CriteriaDigest, &indexed.PolicyDigest, &indexed.Evidence, &indexed.EvidenceDigest, &indexed.ActorID, &indexed.ActorType, &created, &body) != nil {
			return nil, 0, ErrWorkboardCorrupt
		}
		indexed.Version, indexed.BoardID, indexed.CardID, indexed.AttemptID, indexed.CreatedAt = 1, boardID, cardID, attemptID, time.Unix(0, created).UTC()
		if strictJSON(body, &checkpoint) != nil || checkpoint.Validate() != nil || !reflect.DeepEqual(checkpoint, indexed) {
			return nil, 0, ErrWorkboardCorrupt
		}
		reversed = append(reversed, checkpoint)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	result := make([]workboard.CheckpointRecord, len(reversed))
	for index := range reversed {
		result[len(reversed)-1-index] = reversed[index]
	}
	return result, count, nil
}
