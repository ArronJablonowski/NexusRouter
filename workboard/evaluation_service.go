package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	EvaluationMutationVersion = 1
	MaxEvaluationEvidence     = 100
	MaxCandidateArtifacts     = 32
)

type EvaluationKind string

const (
	EvaluationCandidateSubmit EvaluationKind = "candidate.submit"
	EvaluationAccept          EvaluationKind = "acceptance.accept"
	EvaluationReject          EvaluationKind = "acceptance.reject"
)

type EvidenceInput struct {
	CriterionID string `json:"criterion_id"`
	Source      string `json:"source"`
	Outcome     string `json:"outcome"`
	ActorID     string `json:"actor_id"`
	ActorType   string `json:"actor_type"`
	Reference   string `json:"reference"`
}

func (e EvidenceInput) Validate() error {
	if !validID(e.CriterionID) || !validID(e.ActorID) || !validID(e.Reference) ||
		(e.Outcome != "passed" && e.Outcome != "failed" && e.Outcome != "abstained") {
		return fail(CodeInvalid, "evidence")
	}
	switch e.Source {
	case "deterministic":
		if e.ActorType != "validator" || e.Outcome == "abstained" {
			return fail(CodeInvalid, "evidence")
		}
	case "user_feedback":
		if e.ActorType != "operator" || e.Outcome == "abstained" {
			return fail(CodeInvalid, "evidence")
		}
	case "model_audit":
		if e.ActorType != "model" {
			return fail(CodeInvalid, "evidence")
		}
	default:
		return fail(CodeInvalid, "evidence")
	}
	return nil
}

type EvidenceRecord struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	Revision        int64     `json:"revision"`
	BoardID         string    `json:"board_id"`
	CardID          string    `json:"card_id"`
	AttemptID       string    `json:"attempt_id"`
	CandidateID     string    `json:"candidate_id"`
	CriterionID     string    `json:"criterion_id"`
	Source          string    `json:"source"`
	Outcome         string    `json:"outcome"`
	ActorID         string    `json:"actor_id"`
	ActorType       string    `json:"actor_type"`
	Reference       string    `json:"reference"`
	CandidateDigest string    `json:"candidate_digest"`
	CriteriaDigest  string    `json:"criteria_digest"`
	PolicyDigest    string    `json:"policy_digest"`
	CreatedAt       time.Time `json:"created_at"`
}

func (e EvidenceRecord) Validate() error {
	input := EvidenceInput{CriterionID: e.CriterionID, Source: e.Source, Outcome: e.Outcome, ActorID: e.ActorID, ActorType: e.ActorType, Reference: e.Reference}
	if e.Version != SchemaVersion || e.Revision < 1 || !validLifecycleIDs(e.ID, e.BoardID, e.CardID, e.AttemptID, e.CandidateID) ||
		input.Validate() != nil || !digest(e.CandidateDigest) || !digest(e.CriteriaDigest) || !digest(e.PolicyDigest) || !validTime(e.CreatedAt) {
		return fail(CodeInvalid, "evidence")
	}
	return nil
}

type CandidateRecord struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	BoardID        string    `json:"board_id"`
	CardID         string    `json:"card_id"`
	AttemptID      string    `json:"attempt_id"`
	Revision       int64     `json:"revision"`
	Digest         string    `json:"digest"`
	CriteriaDigest string    `json:"criteria_digest"`
	PolicyDigest   string    `json:"policy_digest"`
	EvidenceDigest string    `json:"evidence_digest"`
	EvidenceCount  int       `json:"evidence_count"`
	Summary        string    `json:"summary"`
	ArtifactRefs   []string  `json:"artifact_refs"`
	SubmittedBy    string    `json:"submitted_by"`
	CreatedAt      time.Time `json:"created_at"`
}

func (c CandidateRecord) Validate() error {
	if c.Version != SchemaVersion || !validLifecycleIDs(c.ID, c.BoardID, c.CardID, c.AttemptID, c.SubmittedBy) || c.Revision < 1 ||
		!digest(c.Digest) || !digest(c.CriteriaDigest) || !digest(c.PolicyDigest) || !digest(c.EvidenceDigest) ||
		c.EvidenceCount < 0 || c.EvidenceCount > MaxEvaluationEvidence || !boundedText(c.Summary, MaxDescriptionBytes, false) ||
		!validIDs(c.ArtifactRefs, MaxCandidateArtifacts) || !validTime(c.CreatedAt) {
		return fail(CodeInvalid, "candidate")
	}
	return nil
}

type AcceptanceRecord struct {
	Version                   int       `json:"version"`
	ID                        string    `json:"id"`
	BoardID                   string    `json:"board_id"`
	CardID                    string    `json:"card_id"`
	AttemptID                 string    `json:"attempt_id"`
	CandidateID               string    `json:"candidate_id"`
	CandidateDigest           string    `json:"candidate_digest"`
	CriteriaRevision          int64     `json:"criteria_revision"`
	CriteriaDigest            string    `json:"criteria_digest"`
	PriorEvidenceHeadRevision int64     `json:"prior_evidence_head_revision"`
	PriorEvidenceSetDigest    string    `json:"prior_evidence_set_digest"`
	EvidenceHeadRevision      int64     `json:"evidence_head_revision"`
	EvidenceSetDigest         string    `json:"evidence_set_digest"`
	PolicyDigest              string    `json:"policy_digest"`
	Decision                  string    `json:"decision"`
	DecidedBy                 string    `json:"decided_by"`
	DecidedByType             string    `json:"decided_by_type"`
	DecisionAuthorityID       string    `json:"decision_authority_id"`
	Rationale                 string    `json:"rationale"`
	DecidedAt                 time.Time `json:"decided_at"`
}

func (a AcceptanceRecord) Validate() error {
	if a.Version != SchemaVersion || !validLifecycleIDs(a.ID, a.BoardID, a.CardID, a.AttemptID, a.CandidateID, a.DecidedBy, a.DecisionAuthorityID) ||
		!digest(a.CandidateDigest) || a.CriteriaRevision < 1 || a.PriorEvidenceHeadRevision < 0 || a.EvidenceHeadRevision < a.PriorEvidenceHeadRevision ||
		!digest(a.CriteriaDigest) || !digest(a.PriorEvidenceSetDigest) || !digest(a.EvidenceSetDigest) ||
		!digest(a.PolicyDigest) || (a.Decision != "accepted" && a.Decision != "rejected") ||
		(a.DecidedByType != "operator" && a.DecidedByType != "validator") || !boundedText(a.Rationale, MaxCheckpointBytes, false) || !validTime(a.DecidedAt) {
		return fail(CodeInvalid, "acceptance")
	}
	return nil
}

type SubmitCandidateRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey           string
	ExpectedCardRevision, ExpectedClaimRevision, CriteriaRevision int64
	Summary                                                       string
	ArtifactRefs                                                  []string
}

// CandidateEvaluationRequest is the immutable, host-derived snapshot an
// evaluator receives. It binds the exact candidate content to the durable
// worker attempt and acceptance policy; evaluators never derive identity from
// model output or transport input.
type CandidateEvaluationRequest struct {
	Version                                               int
	BoardID, CardID, AttemptID, ClaimID, CandidateID      string
	BindingKind                                           string
	SourceTaskID, SourceSessionID, SourceTurnID           string
	SourceAttemptID, SourceCompletionEventID              string
	SourceCompletionSequence                              int64
	SourceCompletionDigest, SourceOutputDigest            string
	SourceTerminalEventID                                 string
	SourceTerminalSequence                                int64
	SourceTerminalDigest                                  string
	SourceDomain, SourceProfile, SourcePrivacy            string
	AdmissionID, AdmissionDigest                          string
	SourceModelID, SourceProviderID, ConfigID             string
	SourceTimeLimitMS, SourceTokenLimit, SourceCostMicros int64
	WorkerID                                              string
	ExpectedCardRevision, ExpectedClaimRevision           int64
	CriteriaRevision                                      int64
	CandidateDigest, CriteriaDigest, PolicyDigest         string
	Summary                                               string
	ArtifactRefs                                          []string
	Criteria                                              []AcceptanceCriterion
}

func (r CandidateEvaluationRequest) Validate() error {
	if r.Version != SchemaVersion || !validLifecycleIDs(r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.CandidateID, r.WorkerID) ||
		(r.SourceTaskID == "") != (r.SourceSessionID == "") ||
		(r.SourceTaskID != "" && !validLifecycleIDs(r.SourceTaskID, r.SourceSessionID)) || r.ExpectedCardRevision < 1 || r.ExpectedClaimRevision < 1 ||
		r.CriteriaRevision < 1 || !digest(r.CandidateDigest) || !digest(r.CriteriaDigest) || !digest(r.PolicyDigest) ||
		(r.ConfigID != "" && !digest(r.ConfigID)) || !boundedText(r.Summary, MaxDescriptionBytes, false) ||
		!validIDs(r.ArtifactRefs, MaxCandidateArtifacts) || validateAcceptanceCriteria(r.Criteria, r.CriteriaRevision) != nil ||
		CandidateContentDigest(r.Summary, r.ArtifactRefs) != r.CandidateDigest || AcceptanceCriteriaDigest(r.Criteria) != r.CriteriaDigest {
		return fail(CodeInvalid, "candidate_evaluation")
	}
	switch r.BindingKind {
	case "legacy":
		if r.SourceTaskID != "" || r.SourceTurnID != "" || r.SourceAttemptID != "" || r.SourceCompletionEventID != "" ||
			r.SourceCompletionSequence != 0 || r.SourceCompletionDigest != "" || r.SourceOutputDigest != "" || r.SourceTerminalEventID != "" ||
			r.SourceTerminalSequence != 0 || r.SourceTerminalDigest != "" || r.SourceDomain != "" ||
			r.SourceProfile != "" || r.SourcePrivacy != "" || r.AdmissionID != "" || r.AdmissionDigest != "" || r.SourceModelID != "" ||
			r.SourceProviderID != "" || r.ConfigID != "" || r.SourceTimeLimitMS != 0 || r.SourceTokenLimit != 0 || r.SourceCostMicros != 0 {
			return fail(CodeInvalid, "candidate_evaluation")
		}
	case "runtime_unbudgeted":
		if r.SourceTaskID == "" || r.SourceTurnID != "" || r.SourceAttemptID != "" || r.SourceCompletionEventID != "" ||
			r.SourceCompletionSequence != 0 || r.SourceCompletionDigest != "" || r.SourceOutputDigest != "" || r.SourceTerminalEventID != "" ||
			r.SourceTerminalSequence != 0 || r.SourceTerminalDigest != "" || r.SourceDomain != "" || r.SourceProfile != "" ||
			r.SourcePrivacy != "" || r.AdmissionID != "" || r.AdmissionDigest != "" || r.SourceModelID != "" ||
			r.SourceProviderID != "" || r.ConfigID != "" || r.SourceTimeLimitMS != 0 || r.SourceTokenLimit != 0 || r.SourceCostMicros != 0 {
			return fail(CodeInvalid, "candidate_evaluation")
		}
	case "runtime_budgeted":
		if r.SourceTaskID == "" || !validLifecycleIDs(r.SourceTurnID, r.SourceAttemptID, r.SourceCompletionEventID, r.SourceTerminalEventID) ||
			r.SourceCompletionSequence < 1 || !digest(r.SourceCompletionDigest) || !digest(r.SourceOutputDigest) ||
			r.SourceTerminalSequence <= r.SourceCompletionSequence || !digest(r.SourceTerminalDigest) ||
			!boundedText(r.SourceDomain, MaxIdentifierBytes, true) || !boundedText(r.SourceProfile, MaxIdentifierBytes, true) ||
			!boundedText(r.SourcePrivacy, MaxIdentifierBytes, true) ||
			!validLifecycleIDs(r.AdmissionID, r.SourceProviderID) || !boundedText(r.SourceModelID, MaxExecutionModelBytes, false) ||
			!digest(r.AdmissionDigest) || !digest(r.ConfigID) || r.SourceTimeLimitMS < 0 || r.SourceTimeLimitMS > MaxWorkDurationMillis ||
			r.SourceTokenLimit < 0 || r.SourceTokenLimit > MaxWorkTokens || r.SourceCostMicros < 0 || r.SourceCostMicros > MaxWorkCostMicros {
			return fail(CodeInvalid, "candidate_evaluation")
		}
	default:
		return fail(CodeInvalid, "candidate_evaluation")
	}
	return nil
}

type DecideCandidateRequest struct {
	BoardID, CardID, AttemptID, CandidateID, IdempotencyKey          string
	ExpectedCardRevision, CriteriaRevision, EvidenceHeadRevision     int64
	CandidateDigest, CriteriaDigest, EvidenceSetDigest, PolicyDigest string
	Evidence                                                         string
}

type EvaluationMutation struct {
	Version               int             `json:"version"`
	Kind                  EvaluationKind  `json:"kind"`
	BoardID               string          `json:"board_id"`
	CardID                string          `json:"card_id"`
	AttemptID             string          `json:"attempt_id"`
	ClaimID               string          `json:"claim_id,omitempty"`
	CandidateID           string          `json:"candidate_id,omitempty"`
	IdempotencyKey        string          `json:"-"`
	RequestDigest         string          `json:"-"`
	Actor                 Actor           `json:"actor"`
	DecisionAuthorityID   string          `json:"decision_authority_id,omitempty"`
	ExpectedCardRevision  int64           `json:"expected_card_revision"`
	ExpectedClaimRevision int64           `json:"expected_claim_revision,omitempty"`
	CriteriaRevision      int64           `json:"criteria_revision"`
	EvidenceHeadRevision  int64           `json:"evidence_head_revision,omitempty"`
	CandidateDigest       string          `json:"candidate_digest,omitempty"`
	CriteriaDigest        string          `json:"criteria_digest,omitempty"`
	EvidenceSetDigest     string          `json:"evidence_set_digest,omitempty"`
	PolicyDigest          string          `json:"policy_digest,omitempty"`
	Summary               string          `json:"summary,omitempty"`
	ArtifactRefs          []string        `json:"artifact_refs,omitempty"`
	Evidence              string          `json:"evidence,omitempty"`
	Evaluated             []EvidenceInput `json:"-"`
	Now                   time.Time       `json:"-"`
}

type CandidateEvaluator interface {
	EvaluateCandidate(context.Context, CandidateEvaluationRequest) ([]EvidenceInput, error)
}

// BudgetedCandidateEvaluation is the terminal result of one independently
// budgeted advisory review. Measurements are trusted host/provider facts, not
// evaluator-authored output.
type BudgetedCandidateEvaluation struct {
	Evidence     []EvidenceInput
	Measurements AuxiliaryReviewMeasurements
}

// BudgetedCandidateEvaluator opts a candidate evaluator into durable review
// admission. EvaluateCandidate remains part of the interface for compatibility
// with callers that use the evaluator outside this service; this service calls
// only EvaluateBudgetedCandidate after admission succeeds.
type BudgetedCandidateEvaluator interface {
	CandidateEvaluator
	AuxiliaryReviewReservation(CandidateEvaluationRequest) (AuxiliaryReviewReservation, error)
	EvaluateBudgetedCandidate(context.Context, CandidateEvaluationRequest) (BudgetedCandidateEvaluation, error)
}

type AuxiliaryReviewRepository interface {
	AdmitAuxiliaryReview(context.Context, CandidateEvaluationRequest, AuxiliaryReviewReservation, string, func() time.Time) (AuxiliaryReviewAdmissionRecord, bool, error)
	ReplayAuxiliaryReviewAdmission(context.Context, string) (AuxiliaryReviewAdmissionRecord, bool, error)
	SettleAuxiliaryReview(context.Context, string, AuxiliaryReviewDisposition, AuxiliaryReviewMeasurements, time.Time) (AuxiliaryReviewSettlementRecord, bool, error)
	ReplayAuxiliaryReviewSettlement(context.Context, string, AuxiliaryReviewDisposition) (AuxiliaryReviewSettlementRecord, bool, error)
	ApplyEvaluationMutationAndSettleAuxiliaryReview(context.Context, EvaluationMutation, func() time.Time, string, AuxiliaryReviewMeasurements) (OperationReceipt, AuxiliaryReviewSettlementRecord, error)
}

type EvaluationRepository interface {
	PrepareCandidateEvaluation(context.Context, EvaluationMutation) (CandidateEvaluationRequest, error)
	ApplyEvaluationMutation(context.Context, EvaluationMutation, func() time.Time) (OperationReceipt, error)
	ReplayEvaluationMutation(context.Context, EvaluationMutation) (OperationReceipt, bool, error)
}

type EvaluationService struct {
	repository EvaluationRepository
	authority  AuthoritySource
	evaluator  CandidateEvaluator
	auxiliary  AuxiliaryReviewRepository
	now        func() time.Time
	mu         sync.Mutex
	inflight   map[string]*evaluationCall
}

type evaluationCall struct {
	done          chan struct{}
	requestDigest string
	receipt       OperationReceipt
	err           error
}

func NewEvaluationService(repository EvaluationRepository, authority AuthoritySource, evaluator CandidateEvaluator, now func() time.Time) (*EvaluationService, error) {
	if repository == nil || authority == nil || evaluator == nil || now == nil {
		return nil, fail(CodeInvalid, "service")
	}
	var auxiliary AuxiliaryReviewRepository
	if _, budgeted := evaluator.(BudgetedCandidateEvaluator); budgeted {
		var ok bool
		auxiliary, ok = repository.(AuxiliaryReviewRepository)
		if !ok {
			return nil, fail(CodeInvalid, "auxiliary_review_repository")
		}
	}
	return &EvaluationService{repository: repository, authority: authority, evaluator: evaluator, auxiliary: auxiliary, now: now, inflight: map[string]*evaluationCall{}}, nil
}

func (s *EvaluationService) SubmitCandidate(ctx context.Context, request SubmitCandidateRequest) (OperationReceipt, error) {
	if request.ArtifactRefs == nil {
		request.ArtifactRefs = []string{}
	}
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 ||
		request.ExpectedClaimRevision < 1 || request.CriteriaRevision < 1 || !boundedText(request.Summary, MaxDescriptionBytes, false) || !validIDs(request.ArtifactRefs, MaxCandidateArtifacts) {
		return OperationReceipt{}, fail(CodeInvalid, "candidate")
	}
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil || authority.Actor.Type != "worker" {
		return OperationReceipt{}, fail(CodeInvalid, "authority")
	}
	mutation := EvaluationMutation{Version: 1, Kind: EvaluationCandidateSubmit, BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID,
		ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: authority.Actor, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedClaimRevision: request.ExpectedClaimRevision, CriteriaRevision: request.CriteriaRevision, Summary: request.Summary,
		ArtifactRefs: append([]string{}, request.ArtifactRefs...), Now: s.now().UTC()}
	mutation.CandidateID = candidateIdentity(mutation)
	mutation.CandidateDigest = CandidateContentDigest(mutation.Summary, mutation.ArtifactRefs)
	mutation.RequestDigest, err = EvaluationDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	flightKey := evaluationFlightKey(mutation)
	s.mu.Lock()
	if active := s.inflight[flightKey]; active != nil {
		s.mu.Unlock()
		if active.requestDigest != mutation.RequestDigest {
			return OperationReceipt{}, fail(CodeInvalid, "idempotency_key")
		}
		select {
		case <-ctx.Done():
			return OperationReceipt{}, ctx.Err()
		case <-active.done:
			return active.receipt, active.err
		}
	}
	active := &evaluationCall{done: make(chan struct{}), requestDigest: mutation.RequestDigest}
	s.inflight[flightKey] = active
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, flightKey)
		close(active.done)
		s.mu.Unlock()
	}()
	if receipt, found, replayErr := s.repository.ReplayEvaluationMutation(ctx, mutation); found || replayErr != nil {
		if found && replayErr == nil {
			replayErr = s.reconcileAuxiliaryReview(ctx, mutation)
		}
		active.receipt, active.err = receipt, replayErr
		return receipt, replayErr
	}
	frozen, err := s.repository.PrepareCandidateEvaluation(ctx, mutation)
	if err != nil || frozen.Validate() != nil || frozen.CandidateID != mutation.CandidateID || frozen.CandidateDigest != mutation.CandidateDigest ||
		frozen.WorkerID != authority.Actor.ID || frozen.BoardID != request.BoardID || frozen.CardID != request.CardID ||
		frozen.AttemptID != request.AttemptID || frozen.ClaimID != request.ClaimID || frozen.ExpectedCardRevision != request.ExpectedCardRevision ||
		frozen.ExpectedClaimRevision != request.ExpectedClaimRevision || frozen.CriteriaRevision != request.CriteriaRevision ||
		frozen.Summary != request.Summary || !sameStrings(frozen.ArtifactRefs, request.ArtifactRefs) {
		if err == nil {
			err = fail(CodeInvalid, "candidate_evaluation")
		}
		active.err = err
		return OperationReceipt{}, err
	}
	var reviewOperation string
	var reviewReservation AuxiliaryReviewReservation
	var measurements AuxiliaryReviewMeasurements
	if budgeted, ok := s.evaluator.(BudgetedCandidateEvaluator); ok {
		reservation, reserveErr := prepareAuxiliaryReviewReservation(budgeted, frozen)
		if reserveErr != nil || reservation.Validate() != nil || !auxiliaryReservationMatchesFrozen(reservation, frozen) {
			if reserveErr == nil {
				reserveErr = fail(CodeInvalid, "auxiliary_review_reservation")
			}
			active.err = reserveErr
			return OperationReceipt{}, reserveErr
		}
		reviewReservation = reservation
		reviewOperation = auxiliaryReviewOperationID(frozen)
		_, created, admitErr := s.auxiliary.AdmitAuxiliaryReview(ctx, frozen, reservation, reviewOperation, s.now)
		if admitErr != nil {
			active.err = admitErr
			return OperationReceipt{}, admitErr
		}
		if !created {
			active.err = fail(CodeIllegalTransition, "auxiliary_review_inflight")
			return OperationReceipt{}, active.err
		}
		reviewCtx, cancelReview := context.WithTimeout(ctx, time.Duration(reservation.TimeLimitMS)*time.Millisecond)
		result, evaluateErr := invokeBudgetedCandidateEvaluator(reviewCtx, budgeted, frozen)
		reviewDeadlineErr := reviewCtx.Err()
		cancelReview()
		if evaluateErr == nil && reviewDeadlineErr != nil {
			evaluateErr = reviewDeadlineErr
		}
		if evaluateErr == nil && !auxiliaryMeasurementsWithinReservation(result.Measurements, reservation) {
			evaluateErr = fail(CodeLimitExceeded, "auxiliary_review_measurements")
		}
		mutation.Evaluated, measurements, err = result.Evidence, result.Measurements, evaluateErr
	} else {
		mutation.Evaluated, err = s.evaluator.EvaluateCandidate(ctx, frozen)
	}
	if err == nil && reviewOperation != "" {
		for _, evidence := range mutation.Evaluated {
			if evidence.Source == "user_feedback" ||
				evidence.Source == "model_audit" && evidence.ActorID != reviewReservation.ReviewerID {
				err = fail(CodeInvalid, "evidence")
				break
			}
		}
	}
	if err != nil {
		if reviewOperation != "" {
			disposition := AuxiliaryReviewFailed
			if ctx.Err() != nil {
				disposition = AuxiliaryReviewCanceled
			}
			err = errors.Join(err, s.settleAuxiliaryReview(ctx, reviewOperation, disposition, measurements))
		}
		active.err = err
		return OperationReceipt{}, err
	}
	for _, evidence := range mutation.Evaluated {
		// User feedback is an operator review action, never output from a
		// worker-side candidate evaluator. Repositories enforce the same rule
		// at their durable boundary as defense in depth.
		if evidence.Source == "user_feedback" {
			active.err = fail(CodeInvalid, "evidence")
			return OperationReceipt{}, active.err
		}
	}
	// The evaluator may have taken long enough for the claim to expire or for
	// operator control to advance the card. Commit against fresh trusted time;
	// repositories recheck every revision and lease fence transactionally.
	mutation.Now = s.now().UTC()
	if reviewOperation != "" {
		var settlement AuxiliaryReviewSettlementRecord
		active.receipt, settlement, active.err = s.auxiliary.ApplyEvaluationMutationAndSettleAuxiliaryReview(ctx, mutation, s.now, reviewOperation, measurements)
		if active.err != nil {
			active.err = errors.Join(active.err, s.settleAuxiliaryReview(ctx, reviewOperation, AuxiliaryReviewFailed, measurements))
		} else if active.receipt.Validate() != nil || active.receipt.BoardID != mutation.BoardID || active.receipt.CardID != mutation.CardID ||
			settlement.Validate() != nil || settlement.OperationID != reviewOperation || settlement.Disposition != AuxiliaryReviewCompleted ||
			settlement.BoardID != mutation.BoardID || settlement.CardID != mutation.CardID || settlement.AttemptID != mutation.AttemptID ||
			settlement.ClaimID != mutation.ClaimID || settlement.CandidateID != mutation.CandidateID || settlement.CandidateDigest != mutation.CandidateDigest {
			active.receipt = OperationReceipt{}
			active.err = fail(CodeInvalid, "stored_auxiliary_review_commit")
		}
	} else {
		active.receipt, active.err = s.apply(ctx, mutation)
	}
	return active.receipt, active.err
}

func invokeBudgetedCandidateEvaluator(ctx context.Context, evaluator BudgetedCandidateEvaluator,
	frozen CandidateEvaluationRequest,
) (result BudgetedCandidateEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result, err = BudgetedCandidateEvaluation{}, fail(CodeInvalid, "auxiliary_review_panic")
		}
	}()
	return evaluator.EvaluateBudgetedCandidate(ctx, frozen)
}

func prepareAuxiliaryReviewReservation(evaluator BudgetedCandidateEvaluator,
	frozen CandidateEvaluationRequest,
) (reservation AuxiliaryReviewReservation, err error) {
	defer func() {
		if recover() != nil {
			reservation, err = AuxiliaryReviewReservation{}, fail(CodeInvalid, "auxiliary_review_reservation_panic")
		}
	}()
	return evaluator.AuxiliaryReviewReservation(frozen)
}

func auxiliaryMeasurementsWithinReservation(m AuxiliaryReviewMeasurements, r AuxiliaryReviewReservation) bool {
	return optionalAuxiliaryMeasurementWithin(m.TimeMS, r.TimeLimitMS) &&
		optionalAuxiliaryMeasurementWithin(m.Tokens, r.TokenLimit) &&
		optionalAuxiliaryMeasurementWithin(m.CostMicros, r.CostMicros)
}

func optionalAuxiliaryMeasurementWithin(measured *int64, limit int64) bool {
	return measured == nil || *measured >= 0 && *measured <= limit
}

func safeAuxiliaryMeasurements(m AuxiliaryReviewMeasurements) AuxiliaryReviewMeasurements {
	if m.TimeMS != nil && (*m.TimeMS < 0 || *m.TimeMS > MaxAuxiliaryReviewDurationMillis) {
		m.TimeMS = nil
	}
	if m.Tokens != nil && (*m.Tokens < 0 || *m.Tokens > MaxWorkTokens) {
		m.Tokens = nil
	}
	if m.CostMicros != nil && (*m.CostMicros < 0 || *m.CostMicros > MaxWorkCostMicros) {
		m.CostMicros = nil
	}
	return m
}

func auxiliaryReviewOperationID(frozen CandidateEvaluationRequest) string {
	return "candidate-review-" + frozen.CandidateID
}

func auxiliaryReservationMatchesFrozen(r AuxiliaryReviewReservation, f CandidateEvaluationRequest) bool {
	return r.BoardID == f.BoardID && r.CardID == f.CardID && r.AttemptID == f.AttemptID && r.ClaimID == f.ClaimID &&
		r.CandidateID == f.CandidateID && r.CandidateDigest == f.CandidateDigest && r.CriteriaDigest == f.CriteriaDigest &&
		r.PolicyDigest == f.PolicyDigest
}

func (s *EvaluationService) settleAuxiliaryReview(ctx context.Context, operation string, disposition AuxiliaryReviewDisposition,
	measurements AuxiliaryReviewMeasurements,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _, err := s.auxiliary.SettleAuxiliaryReview(cleanup, operation, disposition, safeAuxiliaryMeasurements(measurements), s.now().UTC())
	return err
}

func (s *EvaluationService) reconcileAuxiliaryReview(ctx context.Context, mutation EvaluationMutation) error {
	if s.auxiliary == nil {
		return nil
	}
	operation := "candidate-review-" + mutation.CandidateID
	if _, admitted, err := s.auxiliary.ReplayAuxiliaryReviewAdmission(ctx, operation); err != nil || !admitted {
		return err
	}
	if _, found, err := s.auxiliary.ReplayAuxiliaryReviewSettlement(ctx, operation, AuxiliaryReviewCompleted); err != nil || found {
		return err
	}
	// Candidate commit proves the review reached the application boundary. If a
	// crash lost terminal measurements, repair accounting conservatively without
	// ever redispatching the reviewer.
	return s.settleAuxiliaryReview(ctx, operation, AuxiliaryReviewCompleted, AuxiliaryReviewMeasurements{})
}

func (s *EvaluationService) AcceptCandidate(ctx context.Context, request DecideCandidateRequest) (OperationReceipt, error) {
	return s.decide(ctx, EvaluationAccept, request)
}
func (s *EvaluationService) RejectCandidate(ctx context.Context, request DecideCandidateRequest) (OperationReceipt, error) {
	return s.decide(ctx, EvaluationReject, request)
}

func (s *EvaluationService) decide(ctx context.Context, kind EvaluationKind, request DecideCandidateRequest) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.CandidateID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 ||
		request.CriteriaRevision < 1 || request.EvidenceHeadRevision < 0 || !digest(request.CandidateDigest) || !digest(request.CriteriaDigest) ||
		!digest(request.EvidenceSetDigest) || !digest(request.PolicyDigest) || !boundedText(request.Evidence, MaxCheckpointBytes, false) {
		return OperationReceipt{}, fail(CodeInvalid, "acceptance")
	}
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil || (authority.Actor.Type != "operator" && authority.Actor.Type != "validator") || !validID(authority.CreationScope) {
		return OperationReceipt{}, fail(CodeInvalid, "authority")
	}
	mutation := EvaluationMutation{Version: 1, Kind: kind, BoardID: request.BoardID, CardID: request.CardID, AttemptID: request.AttemptID,
		CandidateID: request.CandidateID, IdempotencyKey: request.IdempotencyKey, Actor: authority.Actor, DecisionAuthorityID: authority.CreationScope,
		ExpectedCardRevision: request.ExpectedCardRevision, CriteriaRevision: request.CriteriaRevision, EvidenceHeadRevision: request.EvidenceHeadRevision,
		CandidateDigest: request.CandidateDigest, CriteriaDigest: request.CriteriaDigest, EvidenceSetDigest: request.EvidenceSetDigest,
		PolicyDigest: request.PolicyDigest, Evidence: request.Evidence, Now: s.now().UTC()}
	mutation.RequestDigest, err = EvaluationDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt, found, replayErr := s.repository.ReplayEvaluationMutation(ctx, mutation); found || replayErr != nil {
		return receipt, replayErr
	}
	return s.apply(ctx, mutation)
}

func (s *EvaluationService) apply(ctx context.Context, mutation EvaluationMutation) (OperationReceipt, error) {
	receipt, err := s.repository.ApplyEvaluationMutation(ctx, mutation, s.now)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt.Validate() != nil || receipt.BoardID != mutation.BoardID || receipt.CardID != mutation.CardID {
		return OperationReceipt{}, fail(CodeInvalid, "stored_receipt")
	}
	return receipt, nil
}

func EvaluationDigest(m EvaluationMutation) (string, error) {
	m.IdempotencyKey, m.RequestDigest, m.Evaluated, m.Now = "", "", nil, time.Time{}
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func evaluationFlightKey(m EvaluationMutation) string {
	sum := sha256.Sum256([]byte(m.BoardID + "\x00" + m.Actor.ID + "\x00" + m.Actor.Type + "\x00" + m.IdempotencyKey))
	return hex.EncodeToString(sum[:])
}

func candidateIdentity(m EvaluationMutation) string {
	sum := sha256.Sum256([]byte(m.BoardID + "\x00" + m.CardID + "\x00" + m.AttemptID + "\x00" + m.ClaimID + "\x00" +
		m.Actor.ID + "\x00" + m.IdempotencyKey))
	return "candidate-" + hex.EncodeToString(sum[:20])
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func CandidateContentDigest(summary string, artifacts []string) string {
	body, _ := json.Marshal(struct {
		Summary   string   `json:"summary"`
		Artifacts []string `json:"artifacts"`
	}{summary, artifacts})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func EvidenceSetDigest(evidence []EvidenceRecord) string {
	body, _ := json.Marshal(evidence)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
