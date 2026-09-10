package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	EvaluateCandidate(context.Context, SubmitCandidateRequest, Actor) ([]EvidenceInput, error)
}
type EvaluationRepository interface {
	ApplyEvaluationMutation(context.Context, EvaluationMutation) (OperationReceipt, error)
	ReplayEvaluationMutation(context.Context, EvaluationMutation) (OperationReceipt, bool, error)
}

type EvaluationService struct {
	repository EvaluationRepository
	authority  AuthoritySource
	evaluator  CandidateEvaluator
	now        func() time.Time
	mu         sync.Mutex
	inflight   map[string]*evaluationCall
}

type evaluationCall struct {
	done    chan struct{}
	receipt OperationReceipt
	err     error
}

func NewEvaluationService(repository EvaluationRepository, authority AuthoritySource, evaluator CandidateEvaluator, now func() time.Time) (*EvaluationService, error) {
	if repository == nil || authority == nil || evaluator == nil || now == nil {
		return nil, fail(CodeInvalid, "service")
	}
	return &EvaluationService{repository: repository, authority: authority, evaluator: evaluator, now: now, inflight: map[string]*evaluationCall{}}, nil
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
	mutation.RequestDigest, err = EvaluationDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	flightKey := evaluationFlightKey(mutation)
	s.mu.Lock()
	if active := s.inflight[flightKey]; active != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return OperationReceipt{}, ctx.Err()
		case <-active.done:
			return active.receipt, active.err
		}
	}
	active := &evaluationCall{done: make(chan struct{})}
	s.inflight[flightKey] = active
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, flightKey)
		close(active.done)
		s.mu.Unlock()
	}()
	if receipt, found, replayErr := s.repository.ReplayEvaluationMutation(ctx, mutation); found || replayErr != nil {
		active.receipt, active.err = receipt, replayErr
		return receipt, replayErr
	}
	mutation.Evaluated, err = s.evaluator.EvaluateCandidate(ctx, request, authority.Actor)
	if err != nil {
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
	active.receipt, active.err = s.apply(ctx, mutation)
	return active.receipt, active.err
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
	receipt, err := s.repository.ApplyEvaluationMutation(ctx, mutation)
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
	sum := sha256.Sum256([]byte(m.BoardID + "\x00" + m.Actor.ID + "\x00" + m.Actor.Type + "\x00" + m.IdempotencyKey + "\x00" + m.RequestDigest))
	return hex.EncodeToString(sum[:])
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
