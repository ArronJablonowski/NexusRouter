package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

type storedEvaluationAttempt struct {
	Version                  int                        `json:"version"`
	ID                       string                     `json:"id"`
	BoardID                  string                     `json:"board_id"`
	CardID                   string                     `json:"card_id"`
	Ordinal                  int                        `json:"ordinal"`
	Revision                 int64                      `json:"revision"`
	State                    string                     `json:"state"`
	WorkerID                 string                     `json:"worker_id"`
	CriteriaRevision         int64                      `json:"criteria_revision"`
	CriteriaDigest           string                     `json:"criteria_digest"`
	PolicyDigest             string                     `json:"policy_digest"`
	Budget                   storedWorkboardBudget      `json:"budget"`
	Criteria                 []storedWorkboardCriterion `json:"criteria"`
	TaskIDs                  []string                   `json:"task_ids"`
	SessionIDs               []string                   `json:"session_ids"`
	Claim                    storedLifecycleClaim       `json:"claim"`
	Candidate                *workboard.CandidateRecord `json:"candidate,omitempty"`
	Evidence                 []workboard.EvidenceRecord `json:"evidence"`
	AcceptanceID             string                     `json:"acceptance_id,omitempty"`
	DecisionBy               string                     `json:"decision_by,omitempty"`
	DecisionByType           string                     `json:"decision_by_type,omitempty"`
	DecisionAuthorityID      string                     `json:"decision_authority_id,omitempty"`
	AcceptanceEvidenceDigest string                     `json:"acceptance_evidence_digest,omitempty"`
	StartedAt                time.Time                  `json:"started_at"`
	EndedAt                  *time.Time                 `json:"ended_at,omitempty"`
}

func (s *Store) ApplyEvaluationMutation(ctx context.Context, mutation workboard.EvaluationMutation) (workboard.OperationReceipt, error) {
	if err := validateEvaluationMutation(mutation, true); err != nil {
		return workboard.OperationReceipt{}, err
	}
	want, err := workboard.EvaluationDigest(mutation)
	if err != nil || want != mutation.RequestDigest {
		return workboard.OperationReceipt{}, invalidWorkboard("request_digest")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	defer tx.Rollback()
	if err = reserveWorkboardWriter(ctx, tx); err != nil {
		return workboard.OperationReceipt{}, err
	}
	keyDigest := digestBytes([]byte(mutation.IdempotencyKey))
	if receipt, found, replayErr := readEvaluationReplay(ctx, tx, mutation, keyDigest, want); found || replayErr != nil {
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
	var claimRevision *int64
	var durable int
	var storedResult evaluationStoredResult
	switch mutation.Kind {
	case workboard.EvaluationCandidateSubmit:
		var revision int64
		revision, durable, storedResult, err = submitStoredCandidate(ctx, tx, mutation, &board, &card, &cardBody)
		claimRevision = &revision
	case workboard.EvaluationAccept, workboard.EvaluationReject:
		durable, storedResult, err = decideStoredCandidate(ctx, tx, mutation, &board, &card, &cardBody)
	}
	if err != nil {
		return workboard.OperationReceipt{}, err
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
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	event := workboard.BoardEvent{Version: 1, ID: eventID, BoardID: board.ID, Sequence: board.EventSequence, OperationID: operationID,
		Kind: evaluationAction(mutation.Kind), ActorID: mutation.Actor.ID, ActorType: mutation.Actor.Type, CardID: card.ID, CreatedAt: mutation.Now}
	eventBody, err := json.Marshal(event)
	if err != nil || event.Validate() != nil {
		return workboard.OperationReceipt{}, ErrWorkboardCorrupt
	}
	cardRevision := card.Revision
	receipt := workboard.OperationReceipt{Version: 1, BoardID: board.ID, OperationID: operationID, RequestDigest: want, FirstSequence: board.EventSequence,
		LastSequence: board.EventSequence, EventCount: 1, BoardRevision: board.Revision, CardID: card.ID, CardRevision: &cardRevision,
		ClaimRevision: claimRevision, Outcome: "committed", CreatedAt: mutation.Now}
	response, err := finalizeEvaluationReceipt(&receipt, storedResult, durable+len(boardBody)+len(eventBody))
	if err != nil {
		return workboard.OperationReceipt{}, err
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
	if err = insertWorkboardEvent(ctx, tx, eventID, board.ID, event.Sequence, operationID, string(event.Kind), card.ID, mutation.Actor, mutation.Now, eventBody); err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return workboard.OperationReceipt{}, err
	}
	return receipt, nil
}

func submitStoredCandidate(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation, board *workboard.Board, card *workboard.Card, cardBody *storedWorkboardCard) (int64, int, evaluationStoredResult, error) {
	if card.Revision != m.ExpectedCardRevision {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.State != workboard.InProgress || card.CurrentAttemptID != m.AttemptID || card.CurrentClaimID != m.ClaimID || card.CriteriaRevision != m.CriteriaRevision {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "candidate"}
	}
	lease, claim, err := readLifecycleClaim(ctx, tx, m.BoardID, m.CardID, m.AttemptID, m.ClaimID)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	if lease.Revision != m.ExpectedClaimRevision {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "claim_revision"}
	}
	if lease.OwnerID != m.Actor.ID {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeLeaseOwner, Field: "owner"}
	}
	if lease.State != workboard.LeaseActive || !m.Now.Before(lease.ExpiresAt) {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeLeaseExpired, Field: "claim"}
	}
	base, baseBytes, err := readRunningEvaluationAttempt(ctx, tx, m, claim)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	candidateID := newWorkboardID()
	if candidateID == "" {
		return 0, 0, evaluationStoredResult{}, errors.New("secure identifier generation failed")
	}
	candidateDigest := workboard.CandidateContentDigest(m.Summary, m.ArtifactRefs)
	evidence, err := buildCandidateEvidence(m, candidateID, candidateDigest, base)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	candidate := workboard.CandidateRecord{Version: 1, ID: candidateID, BoardID: m.BoardID, CardID: m.CardID, AttemptID: m.AttemptID, Revision: 1,
		Digest: candidateDigest, CriteriaDigest: base.CriteriaDigest, PolicyDigest: base.PolicyDigest, EvidenceDigest: workboard.EvidenceSetDigest(evidence),
		EvidenceCount: len(evidence), Summary: m.Summary, ArtifactRefs: append([]string{}, m.ArtifactRefs...), SubmittedBy: m.Actor.ID, CreatedAt: m.Now}
	if candidate.Validate() != nil {
		return 0, 0, evaluationStoredResult{}, invalidWorkboard("candidate")
	}
	released := m.Now
	claim.Revision++
	claim.State = string(workboard.LeaseReleased)
	claim.ReleasedAt = &released
	claimBytes, err := encodeLifecycle(claim, 16<<10)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	attempt := evaluationAttemptFromBase(base)
	attempt.Revision++
	attempt.State, attempt.Claim, attempt.Candidate, attempt.Evidence, attempt.EndedAt = "review", claim, &candidate, evidence, &released
	attemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workboard_claims SET revision=?,state='released',released_at=?,body=? WHERE id=? AND revision=?`, claim.Revision, released.UnixNano(), claimBytes, claim.ID, m.ExpectedClaimRevision); err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,state='review',ended_at=?,body=? WHERE id=? AND revision=? AND state='running'`, attempt.Revision, released.UnixNano(), attemptBytes, attempt.ID, base.Revision)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	candidateBytes, _ := json.Marshal(candidate)
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_candidates(id,board_id,card_id,attempt_id,revision,digest,criteria_digest,policy_digest,evidence_digest,evidence_count,submitted_by,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.ID, candidate.BoardID, candidate.CardID, candidate.AttemptID, candidate.Revision, candidate.Digest, candidate.CriteriaDigest, candidate.PolicyDigest,
		candidate.EvidenceDigest, candidate.EvidenceCount, candidate.SubmittedBy, candidate.CreatedAt.UnixNano(), candidateBytes); err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	durable := len(baseBytes) + len(attemptBytes) + len(claimBytes) + len(candidateBytes)
	for ordinal, artifact := range candidate.ArtifactRefs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_candidate_artifacts(board_id,card_id,attempt_id,candidate_id,ordinal,artifact_ref) VALUES(?,?,?,?,?,?)`, m.BoardID, m.CardID, m.AttemptID, candidate.ID, ordinal, artifact); err != nil {
			return 0, 0, evaluationStoredResult{}, err
		}
		durable += len(artifact)
	}
	for _, record := range evidence {
		body, _ := json.Marshal(record)
		durable += len(body)
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_evidence(id,board_id,card_id,attempt_id,candidate_id,criterion_id,revision,source,outcome,actor_id,actor_type,reference,candidate_digest,criteria_revision,criteria_digest,policy_digest,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			record.ID, record.BoardID, record.CardID, record.AttemptID, record.CandidateID, record.CriterionID, record.Revision, record.Source, record.Outcome,
			record.ActorID, record.ActorType, record.Reference, record.CandidateDigest, m.CriteriaRevision, record.CriteriaDigest, record.PolicyDigest, record.CreatedAt.UnixNano(), body); err != nil {
			return 0, 0, evaluationStoredResult{}, err
		}
	}
	card.State, card.CurrentClaimID, card.Revision, card.UpdatedAt = workboard.Review, "", card.Revision+1, m.Now
	cardBody.State, cardBody.CurrentClaimID = string(workboard.Review), ""
	*cardBody = updateStoredBody(*cardBody, *card)
	cardBytes, err := writeEvaluationCard(ctx, tx, *card, *cardBody, m.ExpectedCardRevision)
	if err != nil {
		return 0, 0, evaluationStoredResult{}, err
	}
	board.ActiveClaims--
	board.LayoutRevision++
	if board.ActiveClaims < 0 {
		return 0, 0, evaluationStoredResult{}, ErrWorkboardCorrupt
	}
	return claim.Revision, durable + cardBytes, evaluationStoredResult{Candidate: &candidate, Evidence: evidence}, nil
}

func decideStoredCandidate(ctx context.Context, tx *sql.Tx, m workboard.EvaluationMutation, board *workboard.Board, card *workboard.Card, cardBody *storedWorkboardCard) (int, evaluationStoredResult, error) {
	if card.Revision != m.ExpectedCardRevision {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	if card.State != workboard.Review || card.CurrentAttemptID != m.AttemptID || card.CurrentClaimID != "" || card.CriteriaRevision != m.CriteriaRevision || card.RemainingDependencies != 0 {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "acceptance"}
	}
	attempt, oldBytes, err := readReviewEvaluationAttempt(ctx, tx, m)
	if err != nil {
		return 0, evaluationStoredResult{}, err
	}
	if attempt.WorkerID == m.Actor.ID || attempt.Claim.OwnerID == m.Actor.ID {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeInvalid, Field: "independent_accepter"}
	}
	if attempt.Candidate == nil || attempt.Candidate.ID != m.CandidateID || attempt.Candidate.Digest != m.CandidateDigest || attempt.CriteriaRevision != m.CriteriaRevision ||
		attempt.CriteriaDigest != m.CriteriaDigest || attempt.PolicyDigest != m.PolicyDigest || int64(len(attempt.Evidence)) != m.EvidenceHeadRevision ||
		workboard.EvidenceSetDigest(attempt.Evidence) != m.EvidenceSetDigest {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "evaluation_fence"}
	}
	priorHead, priorDigest := int64(len(attempt.Evidence)), workboard.EvidenceSetDigest(attempt.Evidence)
	decision, target, feedbackOutcome := "accepted", workboard.Done, "passed"
	if m.Kind == workboard.EvaluationReject {
		decision, target, feedbackOutcome = "rejected", workboard.Ready, "failed"
	}
	feedback, err := buildReviewFeedback(m, attempt, feedbackOutcome)
	if err != nil {
		return 0, evaluationStoredResult{}, err
	}
	attempt.Evidence = append(attempt.Evidence, feedback...)
	accepted := evidenceAllowsAcceptance(domainCriteria(attempt.Criteria), attempt.Evidence)
	rejected := evidenceRequiresRejection(domainCriteria(attempt.Criteria), attempt.Evidence)
	if m.Kind == workboard.EvaluationAccept && !accepted || m.Kind == workboard.EvaluationReject && !rejected {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeIllegalTransition, Field: "evidence"}
	}
	acceptanceID := newWorkboardID()
	if acceptanceID == "" {
		return 0, evaluationStoredResult{}, errors.New("secure identifier generation failed")
	}
	finalHead, finalDigest := int64(len(attempt.Evidence)), workboard.EvidenceSetDigest(attempt.Evidence)
	record := workboard.AcceptanceRecord{Version: 1, ID: acceptanceID, BoardID: m.BoardID, CardID: m.CardID, AttemptID: m.AttemptID,
		CandidateID: m.CandidateID, CandidateDigest: m.CandidateDigest, CriteriaRevision: m.CriteriaRevision, CriteriaDigest: m.CriteriaDigest,
		PriorEvidenceHeadRevision: priorHead, PriorEvidenceSetDigest: priorDigest, EvidenceHeadRevision: finalHead, EvidenceSetDigest: finalDigest,
		PolicyDigest: m.PolicyDigest, Decision: decision, DecidedBy: m.Actor.ID, DecidedByType: m.Actor.Type,
		DecisionAuthorityID: m.DecisionAuthorityID, Rationale: m.Evidence, DecidedAt: m.Now}
	if record.Validate() != nil {
		return 0, evaluationStoredResult{}, ErrWorkboardCorrupt
	}
	attempt.Revision++
	attempt.State, attempt.AcceptanceID, attempt.DecisionBy, attempt.DecisionByType = decision, acceptanceID, m.Actor.ID, m.Actor.Type
	attempt.DecisionAuthorityID, attempt.AcceptanceEvidenceDigest, attempt.EndedAt = m.DecisionAuthorityID, finalDigest, &m.Now
	attemptBytes, err := encodeLifecycle(attempt, workboard.MaxTransactionBytes)
	if err != nil {
		return 0, evaluationStoredResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_attempts SET revision=?,state=?,ended_at=?,body=? WHERE id=? AND revision=? AND state='review'`, attempt.Revision, decision, m.Now.UnixNano(), attemptBytes, attempt.ID, attempt.Revision-1)
	if err != nil {
		return 0, evaluationStoredResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, evaluationStoredResult{}, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "attempt_revision"}
	}
	durable := len(oldBytes) + len(attemptBytes)
	for _, item := range feedback {
		itemBytes, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return 0, evaluationStoredResult{}, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_evidence(id,board_id,card_id,attempt_id,candidate_id,criterion_id,revision,source,outcome,actor_id,actor_type,reference,candidate_digest,criteria_revision,criteria_digest,policy_digest,created_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, item.BoardID, item.CardID, item.AttemptID, item.CandidateID, item.CriterionID, item.Revision, item.Source, item.Outcome,
			item.ActorID, item.ActorType, item.Reference, item.CandidateDigest, m.CriteriaRevision, item.CriteriaDigest, item.PolicyDigest, item.CreatedAt.UnixNano(), itemBytes); err != nil {
			return 0, evaluationStoredResult{}, err
		}
		durable += len(itemBytes)
	}
	recordBytes, _ := json.Marshal(record)
	if _, err = tx.ExecContext(ctx, `INSERT INTO workboard_acceptances(id,board_id,card_id,attempt_id,candidate_id,candidate_digest,criteria_revision,criteria_digest,evidence_head_revision,evidence_set_digest,policy_digest,decision,decided_by,decided_by_type,decision_authority_id,decided_at,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.BoardID, record.CardID, record.AttemptID, record.CandidateID, record.CandidateDigest, record.CriteriaRevision, record.CriteriaDigest,
		record.EvidenceHeadRevision, record.EvidenceSetDigest, record.PolicyDigest, record.Decision, record.DecidedBy, record.DecidedByType,
		record.DecisionAuthorityID, record.DecidedAt.UnixNano(), recordBytes); err != nil {
		return 0, evaluationStoredResult{}, err
	}
	card.State, card.Revision, card.UpdatedAt = target, card.Revision+1, m.Now
	if target == workboard.Done {
		card.AcceptanceID, cardBody.AcceptanceID = acceptanceID, acceptanceID
	} else {
		card.CurrentAttemptID, cardBody.CurrentAttemptID = "", ""
	}
	cardBody.State = string(target)
	*cardBody = updateStoredBody(*cardBody, *card)
	cardBytes, err := writeEvaluationCard(ctx, tx, *card, *cardBody, m.ExpectedCardRevision)
	if err != nil {
		return 0, evaluationStoredResult{}, err
	}
	board.LayoutRevision++
	durable += len(recordBytes) + cardBytes
	return durable, evaluationStoredResult{Candidate: attempt.Candidate, Evidence: attempt.Evidence, Acceptance: &record}, nil
}

func buildReviewFeedback(m workboard.EvaluationMutation, attempt storedEvaluationAttempt, outcome string) ([]workboard.EvidenceRecord, error) {
	if m.Actor.Type != "operator" {
		return []workboard.EvidenceRecord{}, nil
	}
	result := []workboard.EvidenceRecord{}
	for _, criterion := range attempt.Criteria {
		if !criterion.Required || criterion.RequiredSource != "user_feedback" {
			continue
		}
		id, reference := newWorkboardID(), newWorkboardID()
		if id == "" || reference == "" {
			return nil, errors.New("secure identifier generation failed")
		}
		record := workboard.EvidenceRecord{Version: 1, ID: id, Revision: int64(len(attempt.Evidence) + len(result) + 1),
			BoardID: m.BoardID, CardID: m.CardID, AttemptID: m.AttemptID, CandidateID: m.CandidateID, CriterionID: criterion.ID,
			Source: "user_feedback", Outcome: outcome, ActorID: m.Actor.ID, ActorType: m.Actor.Type, Reference: reference,
			CandidateDigest: m.CandidateDigest, CriteriaDigest: m.CriteriaDigest, PolicyDigest: m.PolicyDigest, CreatedAt: m.Now}
		if record.Validate() != nil {
			return nil, invalidWorkboard("feedback")
		}
		result = append(result, record)
	}
	return result, nil
}

func evaluationAttemptFromBase(a storedLifecycleAttempt) storedEvaluationAttempt {
	return storedEvaluationAttempt{Version: a.Version, ID: a.ID, BoardID: a.BoardID, CardID: a.CardID, Ordinal: a.Ordinal, Revision: a.Revision,
		State: a.State, WorkerID: a.WorkerID, CriteriaRevision: a.CriteriaRevision, CriteriaDigest: a.CriteriaDigest, PolicyDigest: a.PolicyDigest,
		Budget: a.Budget, Criteria: a.Criteria, TaskIDs: a.TaskIDs, SessionIDs: a.SessionIDs, Claim: a.Claim, Evidence: []workboard.EvidenceRecord{}, StartedAt: a.StartedAt, EndedAt: a.EndedAt}
}

func buildCandidateEvidence(m workboard.EvaluationMutation, candidateID, candidateDigest string, attempt storedLifecycleAttempt) ([]workboard.EvidenceRecord, error) {
	if len(m.Evaluated) < 1 || len(m.Evaluated) > workboard.MaxEvaluationEvidence {
		return nil, invalidWorkboard("evidence")
	}
	criteria := map[string]storedWorkboardCriterion{}
	for _, criterion := range attempt.Criteria {
		criteria[criterion.ID] = criterion
	}
	result := make([]workboard.EvidenceRecord, len(m.Evaluated))
	for index, input := range m.Evaluated {
		criterion, ok := criteria[input.CriterionID]
		if !ok || input.Validate() != nil || input.Source == "deterministic" && input.ActorID != criterion.ValidatorID {
			return nil, invalidWorkboard("evidence")
		}
		id := newWorkboardID()
		if id == "" {
			return nil, errors.New("secure identifier generation failed")
		}
		result[index] = workboard.EvidenceRecord{Version: 1, ID: id, Revision: int64(index + 1), BoardID: m.BoardID, CardID: m.CardID,
			AttemptID: m.AttemptID, CandidateID: candidateID, CriterionID: input.CriterionID, Source: input.Source, Outcome: input.Outcome,
			ActorID: input.ActorID, ActorType: input.ActorType, Reference: input.Reference, CandidateDigest: candidateDigest,
			CriteriaDigest: attempt.CriteriaDigest, PolicyDigest: attempt.PolicyDigest, CreatedAt: m.Now}
		if result[index].Validate() != nil {
			return nil, invalidWorkboard("evidence")
		}
	}
	return result, nil
}

func evidenceAllowsAcceptance(criteria []workboard.AcceptanceCriterion, evidence []workboard.EvidenceRecord) bool {
	for _, criterion := range criteria {
		if !criterion.Required {
			continue
		}
		passed := false
		for _, item := range evidence {
			if item.CriterionID == criterion.ID && item.Source == criterion.RequiredSource {
				if item.Outcome == "failed" {
					return false
				}
				passed = passed || item.Outcome == "passed"
			}
		}
		if !passed {
			return false
		}
	}
	return true
}

func evidenceRequiresRejection(criteria []workboard.AcceptanceCriterion, evidence []workboard.EvidenceRecord) bool {
	for _, criterion := range criteria {
		if criterion.Required {
			for _, item := range evidence {
				if item.CriterionID == criterion.ID && item.Source == criterion.RequiredSource && item.Outcome == "failed" {
					return true
				}
			}
		}
	}
	return false
}

func domainCriteria(criteria []storedWorkboardCriterion) []workboard.AcceptanceCriterion {
	return domainWorkboardCriteria(criteria)
}

func writeEvaluationCard(ctx context.Context, tx *sql.Tx, card workboard.Card, body storedWorkboardCard, expectedRevision int64) (int, error) {
	body = updateStoredBody(body, card)
	bodyBytes, err := json.Marshal(body)
	if err != nil || card.Validate() != nil || !validStoredCardReferences(body) {
		return 0, ErrWorkboardCorrupt
	}
	result, err := tx.ExecContext(ctx, `UPDATE workboard_cards SET revision=?,state=?,assignee_id=?,block_reason=?,attempt_count=?,current_attempt_id=?,current_claim_id=?,acceptance_id=?,
		cancel_requested=?,pause_requested=?,updated_at=?,body=? WHERE board_id=? AND id=? AND revision=?`, card.Revision, card.State,
		nullable(body.AssigneeID), nullable(body.BlockReason), body.AttemptCount, nullable(body.CurrentAttemptID), nullable(body.CurrentClaimID), nullable(body.AcceptanceID),
		boolInt(body.CancelRequested), boolInt(body.PauseRequested), card.UpdatedAt.UnixNano(), bodyBytes, card.BoardID, card.ID, expectedRevision)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, &workboard.Violation{Code: workboard.CodeStaleRevision, Field: "card_revision"}
	}
	return len(bodyBytes), nil
}

func evaluationAction(kind workboard.EvaluationKind) workboard.BoardAction {
	switch kind {
	case workboard.EvaluationCandidateSubmit:
		return workboard.CandidateSubmitAction
	case workboard.EvaluationAccept:
		return workboard.AcceptanceAcceptAction
	case workboard.EvaluationReject:
		return workboard.AcceptanceRejectAction
	}
	return ""
}
