package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func (s *Store) ReplayEvaluationMutation(ctx context.Context, mutation workboard.EvaluationMutation) (workboard.OperationReceipt, bool, error) {
	if err := validateEvaluationMutation(mutation, false); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	want, err := workboard.EvaluationDigest(mutation)
	if err != nil || want != mutation.RequestDigest {
		return workboard.OperationReceipt{}, false, invalidWorkboard("request_digest")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	defer tx.Rollback()
	receipt, found, err := readEvaluationReplay(ctx, tx, mutation, digestBytes([]byte(mutation.IdempotencyKey)), want)
	if err != nil || !found {
		return workboard.OperationReceipt{}, found, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}

func readEvaluationReplay(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation, keyDigest, requestDigest string) (workboard.OperationReceipt, bool, error) {
	envelope, found, err := readEvaluationResponse(ctx, tx, m.BoardID, keyDigest, requestDigest)
	if err != nil || !found {
		return envelope.Receipt, found, err
	}
	receipt := envelope.Receipt
	if receipt.CardID != m.CardID || receipt.CardRevision == nil || (m.Kind == workboard.EvaluationCandidateSubmit) != (receipt.ClaimRevision != nil) {
		return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
	}
	if err = verifyEvaluationReplayEvents(ctx, tx, m, envelope); err != nil {
		return workboard.OperationReceipt{}, true, err
	}
	if m.Kind == workboard.EvaluationCandidateSubmit {
		candidate, err := readEvaluationCandidate(ctx, tx, m.BoardID, m.CardID, m.AttemptID)
		if err != nil || candidate.ID != m.CandidateID || candidate.Digest != m.CandidateDigest || candidate.SubmittedBy != m.Actor.ID ||
			candidate.Summary != m.Summary || !reflect.DeepEqual(candidate.ArtifactRefs, m.ArtifactRefs) {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
		evidence, evidenceErr := readEvaluationEvidence(ctx, tx, m.BoardID, m.CardID, m.AttemptID, candidate)
		if evidenceErr != nil {
			return workboard.OperationReceipt{}, true, evidenceErr
		}
		if envelope.Result.Acceptance != nil || len(envelope.Result.Successors) != 0 || envelope.Result.Candidate == nil ||
			!reflect.DeepEqual(candidate, *envelope.Result.Candidate) || candidate.EvidenceCount > len(evidence) ||
			!reflect.DeepEqual(evidence[:candidate.EvidenceCount], envelope.Result.Evidence) {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
		if err = validateExecutionSettlementReplay(ctx, tx, m.BoardID, m.CardID, m.AttemptID, m.ClaimID, runtime.TaskCompleted, true); err != nil {
			return workboard.OperationReceipt{}, true, err
		}
	} else {
		if m.Kind == workboard.EvaluationReject && len(envelope.Result.Successors) != 0 {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
		acceptance, err := readEvaluationAcceptance(ctx, tx, m.BoardID, m.CardID, m.AttemptID)
		decision := "accepted"
		if m.Kind == workboard.EvaluationReject {
			decision = "rejected"
		}
		if err != nil || acceptance.CandidateID != m.CandidateID || acceptance.CandidateDigest != m.CandidateDigest || acceptance.CriteriaRevision != m.CriteriaRevision ||
			acceptance.CriteriaDigest != m.CriteriaDigest || acceptance.PriorEvidenceHeadRevision != m.EvidenceHeadRevision || acceptance.PriorEvidenceSetDigest != m.EvidenceSetDigest ||
			acceptance.PolicyDigest != m.PolicyDigest || acceptance.Decision != decision || acceptance.DecidedBy != m.Actor.ID ||
			acceptance.DecisionAuthorityID != m.DecisionAuthorityID || acceptance.Rationale != m.Evidence {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
		candidate, candidateErr := readEvaluationCandidate(ctx, tx, m.BoardID, m.CardID, m.AttemptID)
		evidence, evidenceErr := readEvaluationEvidence(ctx, tx, m.BoardID, m.CardID, m.AttemptID, candidate)
		if candidateErr != nil || evidenceErr != nil || candidate.ID != acceptance.CandidateID || candidate.Digest != acceptance.CandidateDigest ||
			candidate.CriteriaDigest != acceptance.CriteriaDigest || candidate.PolicyDigest != acceptance.PolicyDigest ||
			int64(len(evidence)) != acceptance.EvidenceHeadRevision || workboard.EvidenceSetDigest(evidence) != acceptance.EvidenceSetDigest {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
		if err = verifyEvaluationSuccessorProjections(ctx, tx, envelope.Result.Successors); err != nil {
			return workboard.OperationReceipt{}, true, err
		}
		storedResult := evaluationStoredResult{Candidate: &candidate, Evidence: evidence, Acceptance: &acceptance, Successors: envelope.Result.Successors}
		if !sameEvaluationStoredResult(storedResult, envelope.Result) {
			return workboard.OperationReceipt{}, true, ErrWorkboardCorrupt
		}
	}
	return receipt, true, nil
}

func verifyEvaluationReplayEvents(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation, envelope evaluationMutationResponse) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,sequence,operation_id,kind,actor_id,actor_type,card_id,created_at,body
		FROM workboard_events WHERE board_id=? AND operation_id=? ORDER BY sequence`, m.BoardID, envelope.Receipt.OperationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	expectedCards := []string{m.CardID}
	expectedKinds := []workboard.BoardAction{evaluationAction(m.Kind)}
	expectedIDs := []string{""}
	for _, successor := range envelope.Result.Successors {
		kind, kindErr := successorEffectAction(successor)
		id, idErr := successorEffectEventID(envelope.Receipt.OperationID, m.CardID, successor)
		if kindErr != nil || idErr != nil {
			return ErrWorkboardCorrupt
		}
		expectedCards = append(expectedCards, successor.ID)
		expectedKinds = append(expectedKinds, kind)
		expectedIDs = append(expectedIDs, id)
	}
	count := 0
	for index := 0; rows.Next(); index++ {
		event, scanErr := scanCanonicalWorkboardEvent(rows, m.BoardID)
		if scanErr != nil {
			return scanErr
		}
		if index >= len(expectedCards) || event.Sequence != envelope.Receipt.FirstSequence+int64(index) || event.OperationID != envelope.Receipt.OperationID ||
			event.Kind != expectedKinds[index] || event.CardID != expectedCards[index] || index > 0 && event.ID != expectedIDs[index] ||
			event.ActorID != m.Actor.ID || event.ActorType != m.Actor.Type {
			return ErrWorkboardCorrupt
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(expectedCards) || len(expectedCards) != envelope.Receipt.EventCount {
		return ErrWorkboardCorrupt
	}
	return nil
}

func verifyEvaluationSuccessorProjections(ctx context.Context, tx *sql.Tx, successors []workboard.Card) error {
	for _, committed := range successors {
		current, _, err := readStoredCard(ctx, tx, committed.BoardID, committed.ID)
		if err != nil || current.Revision < committed.Revision || current.Revision == committed.Revision && !reflect.DeepEqual(current, committed) {
			return ErrWorkboardCorrupt
		}
	}
	return nil
}

func readRunningEvaluationAttempt(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation, claim storedLifecycleClaim) (storedLifecycleAttempt, []byte, error) {
	var indexedRevision int64
	var indexedState, workerID, criteriaDigest, policyDigest string
	var ended sql.NullInt64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts WHERE board_id=? AND card_id=? AND id=?`, m.BoardID, m.CardID, m.AttemptID).
		Scan(&indexedRevision, &indexedState, &workerID, &criteriaDigest, &policyDigest, &ended, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardNotFound
	}
	if err != nil {
		return storedLifecycleAttempt{}, nil, err
	}
	var attempt storedLifecycleAttempt
	if strictJSON(body, &attempt) != nil || attempt.Revision != indexedRevision || attempt.State != indexedState || attempt.WorkerID != workerID ||
		attempt.CriteriaDigest != criteriaDigest || attempt.PolicyDigest != policyDigest || ended.Valid || attempt.State != "running" || attempt.WorkerID != m.Actor.ID ||
		attempt.ID != m.AttemptID || attempt.BoardID != m.BoardID || attempt.CardID != m.CardID || attempt.CriteriaRevision != m.CriteriaRevision || !equalStoredClaim(attempt.Claim, claim) {
		return storedLifecycleAttempt{}, nil, ErrWorkboardCorrupt
	}
	return attempt, body, nil
}

func readReviewEvaluationAttempt(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation) (storedEvaluationAttempt, []byte, error) {
	var indexedRevision int64
	var indexedState, workerID, criteriaDigest, policyDigest string
	var ended int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,state,worker_id,criteria_digest,policy_digest,ended_at,body FROM workboard_attempts WHERE board_id=? AND card_id=? AND id=?`, m.BoardID, m.CardID, m.AttemptID).
		Scan(&indexedRevision, &indexedState, &workerID, &criteriaDigest, &policyDigest, &ended, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return storedEvaluationAttempt{}, nil, ErrWorkboardNotFound
	}
	if err != nil {
		return storedEvaluationAttempt{}, nil, err
	}
	var attempt storedEvaluationAttempt
	if strictJSON(body, &attempt) != nil || attempt.Revision != indexedRevision || attempt.State != indexedState || attempt.WorkerID != workerID ||
		attempt.CriteriaDigest != criteriaDigest || attempt.PolicyDigest != policyDigest || attempt.State != "review" || attempt.EndedAt == nil ||
		attempt.EndedAt.UnixNano() != ended || attempt.ID != m.AttemptID || attempt.BoardID != m.BoardID || attempt.CardID != m.CardID ||
		attempt.Candidate == nil || attempt.Candidate.Validate() != nil || workboard.EvidenceSetDigest(attempt.Evidence) != attempt.Candidate.EvidenceDigest {
		return storedEvaluationAttempt{}, nil, ErrWorkboardCorrupt
	}
	storedCandidate, err := readEvaluationCandidate(ctx, tx, m.BoardID, m.CardID, m.AttemptID)
	if err != nil || !reflect.DeepEqual(storedCandidate, *attempt.Candidate) {
		return storedEvaluationAttempt{}, nil, ErrWorkboardCorrupt
	}
	storedEvidence, err := readEvaluationEvidence(ctx, tx, m.BoardID, m.CardID, m.AttemptID, storedCandidate)
	if err != nil || !reflect.DeepEqual(storedEvidence, attempt.Evidence) {
		return storedEvaluationAttempt{}, nil, ErrWorkboardCorrupt
	}
	return attempt, body, nil
}

func readEvaluationCandidate(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.CandidateRecord, error) {
	var indexed workboard.CandidateRecord
	var created int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,revision,digest,criteria_digest,policy_digest,evidence_digest,evidence_count,submitted_by,created_at,body FROM workboard_candidates WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).
		Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.Revision, &indexed.Digest, &indexed.CriteriaDigest, &indexed.PolicyDigest,
			&indexed.EvidenceDigest, &indexed.EvidenceCount, &indexed.SubmittedBy, &created, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.CandidateRecord{}, ErrWorkboardNotFound
	}
	if err != nil {
		return workboard.CandidateRecord{}, err
	}
	indexed.Version, indexed.CreatedAt = 1, time.Unix(0, created).UTC()
	var canonical workboard.CandidateRecord
	if strictJSON(body, &canonical) != nil {
		return workboard.CandidateRecord{}, ErrWorkboardCorrupt
	}
	indexed.Summary, indexed.ArtifactRefs = canonical.Summary, canonical.ArtifactRefs
	if canonical.Validate() != nil || !reflect.DeepEqual(indexed, canonical) {
		return workboard.CandidateRecord{}, ErrWorkboardCorrupt
	}
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,artifact_ref FROM workboard_candidate_artifacts WHERE board_id=? AND card_id=? AND attempt_id=? AND candidate_id=? ORDER BY ordinal`, boardID, cardID, attemptID, indexed.ID)
	if err != nil {
		return workboard.CandidateRecord{}, err
	}
	defer rows.Close()
	artifacts := []string{}
	for rows.Next() {
		var ordinal int
		var value string
		if rows.Scan(&ordinal, &value) != nil || ordinal != len(artifacts) {
			return workboard.CandidateRecord{}, ErrWorkboardCorrupt
		}
		artifacts = append(artifacts, value)
	}
	if rows.Err() != nil || !reflect.DeepEqual(artifacts, canonical.ArtifactRefs) {
		return workboard.CandidateRecord{}, ErrWorkboardCorrupt
	}
	return canonical, nil
}

func readEvaluationEvidence(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string, candidate workboard.CandidateRecord) ([]workboard.EvidenceRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,revision,criterion_id,source,outcome,actor_id,actor_type,reference,candidate_digest,criteria_digest,policy_digest,created_at,body FROM workboard_evidence WHERE board_id=? AND card_id=? AND attempt_id=? AND candidate_id=? ORDER BY revision`, boardID, cardID, attemptID, candidate.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workboard.EvidenceRecord{}
	for rows.Next() {
		var indexed workboard.EvidenceRecord
		var created int64
		var body []byte
		indexed.Version, indexed.BoardID, indexed.CardID, indexed.AttemptID, indexed.CandidateID = 1, boardID, cardID, attemptID, candidate.ID
		if rows.Scan(&indexed.ID, &indexed.Revision, &indexed.CriterionID, &indexed.Source, &indexed.Outcome, &indexed.ActorID, &indexed.ActorType, &indexed.Reference,
			&indexed.CandidateDigest, &indexed.CriteriaDigest, &indexed.PolicyDigest, &created, &body) != nil {
			return nil, ErrWorkboardCorrupt
		}
		indexed.CreatedAt = time.Unix(0, created).UTC()
		var canonical workboard.EvidenceRecord
		if strictJSON(body, &canonical) != nil || canonical.Validate() != nil || !reflect.DeepEqual(indexed, canonical) || canonical.Revision != int64(len(result)+1) {
			return nil, ErrWorkboardCorrupt
		}
		result = append(result, canonical)
	}
	if rows.Err() != nil || len(result) < candidate.EvidenceCount || workboard.EvidenceSetDigest(result[:candidate.EvidenceCount]) != candidate.EvidenceDigest {
		return nil, ErrWorkboardCorrupt
	}
	return result, nil
}

func readEvaluationAcceptance(ctx context.Context, tx *sql.Tx, boardID, cardID, attemptID string) (workboard.AcceptanceRecord, error) {
	var indexed workboard.AcceptanceRecord
	var decided int64
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT id,board_id,card_id,attempt_id,candidate_id,candidate_digest,criteria_revision,criteria_digest,evidence_head_revision,evidence_set_digest,policy_digest,decision,decided_by,decided_by_type,decision_authority_id,decided_at,body FROM workboard_acceptances WHERE board_id=? AND card_id=? AND attempt_id=?`, boardID, cardID, attemptID).
		Scan(&indexed.ID, &indexed.BoardID, &indexed.CardID, &indexed.AttemptID, &indexed.CandidateID, &indexed.CandidateDigest, &indexed.CriteriaRevision,
			&indexed.CriteriaDigest, &indexed.EvidenceHeadRevision, &indexed.EvidenceSetDigest, &indexed.PolicyDigest, &indexed.Decision, &indexed.DecidedBy,
			&indexed.DecidedByType, &indexed.DecisionAuthorityID, &decided, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return workboard.AcceptanceRecord{}, ErrWorkboardNotFound
	}
	if err != nil {
		return workboard.AcceptanceRecord{}, err
	}
	indexed.Version, indexed.DecidedAt = 1, time.Unix(0, decided).UTC()
	var canonical workboard.AcceptanceRecord
	if strictJSON(body, &canonical) != nil {
		return workboard.AcceptanceRecord{}, ErrWorkboardCorrupt
	}
	indexed.PriorEvidenceHeadRevision, indexed.PriorEvidenceSetDigest, indexed.Rationale = canonical.PriorEvidenceHeadRevision, canonical.PriorEvidenceSetDigest, canonical.Rationale
	if canonical.Validate() != nil || !reflect.DeepEqual(indexed, canonical) {
		return workboard.AcceptanceRecord{}, ErrWorkboardCorrupt
	}
	return canonical, nil
}

func validateEvaluationMutation(m workboard.EvaluationMutation, requireEvaluation bool) error {
	if m.Version != 1 || !validWorkboardID(m.BoardID) || !validWorkboardID(m.CardID) || !validWorkboardID(m.AttemptID) || m.Actor.Validate() != nil ||
		len(m.IdempotencyKey) < 16 || len(m.IdempotencyKey) > 128 || !validDigest(m.RequestDigest) || m.ExpectedCardRevision < 1 || m.CriteriaRevision < 1 ||
		(m.AgentApprovalID == "") != (m.AgentProposalDigest == "") ||
		(m.AgentApprovalID != "" && (!validWorkboardID(m.AgentApprovalID) || !validDigest(m.AgentProposalDigest))) ||
		(m.AgentApprovalID == "") != (m.ExpectedBoardRevision == 0 && m.ExpectedAttemptRevision == 0) ||
		(m.AgentApprovalID != "" && (m.ExpectedBoardRevision < 1 || m.ExpectedAttemptRevision < 1)) ||
		m.Now.Location() != time.UTC || m.Now.Year() < 1970 || m.Now.Year() >= 2261 {
		return invalidWorkboard("evaluation")
	}
	for _, r := range m.IdempotencyKey {
		if r < 0x21 || r > 0x7e {
			return invalidWorkboard("evaluation")
		}
	}
	switch m.Kind {
	case workboard.EvaluationCandidateSubmit:
		if m.Actor.Type != "worker" || !validWorkboardID(m.ClaimID) || !validWorkboardID(m.CandidateID) || m.ExpectedClaimRevision < 1 || m.EvidenceHeadRevision != 0 ||
			!validDigest(m.CandidateDigest) || m.CandidateDigest != workboard.CandidateContentDigest(m.Summary, m.ArtifactRefs) ||
			m.CriteriaDigest != "" || m.EvidenceSetDigest != "" || m.PolicyDigest != "" || m.DecisionAuthorityID != "" ||
			m.Summary == "" || len(m.ArtifactRefs) > workboard.MaxCandidateArtifacts || requireEvaluation && len(m.Evaluated) > workboard.MaxEvaluationEvidence || !requireEvaluation && m.Evaluated != nil || m.AgentApprovalID != "" || m.ExpectedBoardRevision != 0 || m.ExpectedAttemptRevision != 0 {
			return invalidWorkboard("candidate")
		}
		for _, item := range m.Evaluated {
			if item.Validate() != nil {
				return invalidWorkboard("evidence")
			}
		}
	case workboard.EvaluationAccept, workboard.EvaluationReject:
		if m.Actor.Type != "operator" && m.Actor.Type != "validator" || !validWorkboardID(m.CandidateID) || !validWorkboardID(m.DecisionAuthorityID) ||
			m.ClaimID != "" || m.ExpectedClaimRevision != 0 || m.EvidenceHeadRevision < 0 || !validDigest(m.CandidateDigest) || !validDigest(m.CriteriaDigest) ||
			!validDigest(m.EvidenceSetDigest) || !validDigest(m.PolicyDigest) || m.Summary != "" || m.ArtifactRefs != nil || m.Evaluated != nil || m.Evidence == "" {
			return invalidWorkboard("acceptance")
		}
	default:
		return invalidWorkboard("evaluation")
	}
	return nil
}
