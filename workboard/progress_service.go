package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	ProgressMutationVersion  = 1
	MaxCheckpointBytes       = 64 << 10
	MaxCheckpointsPerAttempt = 10_000
)

type ProgressKind string

const (
	ProgressCriteriaRevise   ProgressKind = "criteria.revise"
	ProgressCheckpointAppend ProgressKind = "checkpoint.append"
)

type ProgressMutation struct {
	Version                  int                   `json:"version"`
	Kind                     ProgressKind          `json:"kind"`
	BoardID                  string                `json:"board_id"`
	CardID                   string                `json:"card_id"`
	AttemptID                string                `json:"attempt_id,omitempty"`
	ClaimID                  string                `json:"claim_id,omitempty"`
	IdempotencyKey           string                `json:"-"`
	RequestDigest            string                `json:"-"`
	Actor                    Actor                 `json:"actor"`
	ExpectedCardRevision     int64                 `json:"expected_card_revision"`
	ExpectedClaimRevision    int64                 `json:"expected_claim_revision,omitempty"`
	ExpectedCriteriaRevision int64                 `json:"expected_criteria_revision,omitempty"`
	CriteriaRevision         int64                 `json:"criteria_revision,omitempty"`
	Criteria                 []AcceptanceCriterion `json:"criteria,omitempty"`
	Evidence                 string                `json:"evidence,omitempty"`
	Now                      time.Time             `json:"-"`
}

type ReviseCriteriaRequest struct {
	BoardID, CardID, IdempotencyKey string
	ExpectedCardRevision            int64
	ExpectedCriteriaRevision        int64
	Criteria                        []AcceptanceCriterion
}

type AppendCheckpointRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
	CriteriaRevision                                    int64
	Evidence                                            string
}

type CheckpointRecord struct {
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	BoardID          string    `json:"board_id"`
	CardID           string    `json:"card_id"`
	AttemptID        string    `json:"attempt_id"`
	ClaimID          string    `json:"claim_id"`
	Revision         int64     `json:"revision"`
	ClaimRevision    int64     `json:"claim_revision"`
	CriteriaRevision int64     `json:"criteria_revision"`
	CriteriaDigest   string    `json:"criteria_digest"`
	PolicyDigest     string    `json:"policy_digest"`
	Evidence         string    `json:"evidence"`
	EvidenceDigest   string    `json:"evidence_digest"`
	ActorID          string    `json:"actor_id"`
	ActorType        string    `json:"actor_type"`
	CreatedAt        time.Time `json:"created_at"`
}

func (r CheckpointRecord) Validate() error {
	if r.Version != SchemaVersion || !validLifecycleIDs(r.ID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.ActorID) ||
		r.Revision < 1 || r.Revision > MaxCheckpointsPerAttempt || r.ClaimRevision < 1 || r.CriteriaRevision < 1 ||
		!digest(r.CriteriaDigest) || !digest(r.PolicyDigest) || !boundedText(r.Evidence, MaxCheckpointBytes, false) ||
		digestText(r.Evidence) != r.EvidenceDigest || r.ActorType != "worker" || !validTime(r.CreatedAt) {
		return fail(CodeInvalid, "checkpoint")
	}
	return nil
}

type ProgressRepository interface {
	ApplyProgressMutation(context.Context, ProgressMutation) (OperationReceipt, error)
	ReplayProgressMutation(context.Context, ProgressMutation) (OperationReceipt, bool, error)
}

type ProgressService struct {
	repository ProgressRepository
	authority  AuthoritySource
	now        func() time.Time
}

func NewProgressService(repository ProgressRepository, authority AuthoritySource, now func() time.Time) (*ProgressService, error) {
	if repository == nil || authority == nil || now == nil {
		return nil, fail(CodeInvalid, "service")
	}
	return &ProgressService{repository: repository, authority: authority, now: now}, nil
}

func (s *ProgressService) ReviseCriteria(ctx context.Context, request ReviseCriteriaRequest) (OperationReceipt, error) {
	if !validID(request.BoardID) || !validID(request.CardID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 ||
		request.ExpectedCriteriaRevision < 1 || ValidateCriteriaSet(request.Criteria, request.ExpectedCriteriaRevision+1) != nil {
		return OperationReceipt{}, fail(CodeInvalid, "criteria")
	}
	actor, err := s.authorize(ctx, "operator")
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := ProgressMutation{Version: ProgressMutationVersion, Kind: ProgressCriteriaRevise, BoardID: request.BoardID, CardID: request.CardID,
		IdempotencyKey: request.IdempotencyKey, Actor: actor, ExpectedCardRevision: request.ExpectedCardRevision,
		ExpectedCriteriaRevision: request.ExpectedCriteriaRevision, Criteria: copyCriteria(request.Criteria), Now: s.now().UTC()}
	return s.execute(ctx, mutation)
}

func (s *ProgressService) AppendCheckpoint(ctx context.Context, request AppendCheckpointRequest) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) ||
		request.ExpectedCardRevision < 1 || request.ExpectedClaimRevision < 1 || request.CriteriaRevision < 1 ||
		ValidateCheckpointEvidence(request.Evidence) != nil {
		return OperationReceipt{}, fail(CodeInvalid, "checkpoint")
	}
	actor, err := s.authorize(ctx, "worker")
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := ProgressMutation{Version: ProgressMutationVersion, Kind: ProgressCheckpointAppend, BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: actor,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision,
		CriteriaRevision: request.CriteriaRevision, Evidence: request.Evidence, Now: s.now().UTC()}
	return s.execute(ctx, mutation)
}

func (s *ProgressService) execute(ctx context.Context, mutation ProgressMutation) (OperationReceipt, error) {
	digestValue, err := ProgressDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation.RequestDigest = digestValue
	if receipt, found, replayErr := s.repository.ReplayProgressMutation(ctx, mutation); found || replayErr != nil {
		return receipt, replayErr
	}
	receipt, err := s.repository.ApplyProgressMutation(ctx, mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt.Validate() != nil || receipt.BoardID != mutation.BoardID || receipt.CardID != mutation.CardID {
		return OperationReceipt{}, fail(CodeInvalid, "stored_receipt")
	}
	return receipt, nil
}

func (s *ProgressService) authorize(ctx context.Context, actorType string) (Actor, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil {
		return Actor{}, err
	}
	if authority.Validate() != nil || authority.Actor.Type != actorType {
		return Actor{}, fail(CodeInvalid, "authority")
	}
	return authority.Actor, nil
}

func ProgressDigest(mutation ProgressMutation) (string, error) {
	copy := mutation
	copy.IdempotencyKey, copy.RequestDigest, copy.Now = "", "", time.Time{}
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "mutation")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func ValidateCriteriaSet(criteria []AcceptanceCriterion, revision int64) error {
	return validateAcceptanceCriteria(criteria, revision)
}

func ValidateCheckpointEvidence(evidence string) error {
	if !boundedText(evidence, MaxCheckpointBytes, false) || strings.TrimSpace(evidence) == "" {
		return fail(CodeInvalid, "evidence")
	}
	return nil
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
