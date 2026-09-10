package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const LifecycleMutationVersion = 1

type LifecycleKind string

const (
	LifecycleClaim     LifecycleKind = "card.claim"
	LifecycleHeartbeat LifecycleKind = "claim.heartbeat"
	LifecycleRecover   LifecycleKind = "claim.recover"
)

// LifecycleMutation is the replay-bound command passed to durable storage.
// Now and verifier conclusions are execution observations, not client intent,
// and are deliberately excluded from the semantic request digest.
type LifecycleMutation struct {
	Version               int             `json:"version"`
	Kind                  LifecycleKind   `json:"kind"`
	BoardID               string          `json:"board_id"`
	CardID                string          `json:"card_id"`
	AttemptID             string          `json:"attempt_id,omitempty"`
	ClaimID               string          `json:"claim_id,omitempty"`
	IdempotencyKey        string          `json:"-"`
	RequestDigest         string          `json:"-"`
	Actor                 Actor           `json:"actor"`
	ExpectedCardRevision  int64           `json:"expected_card_revision,omitempty"`
	ExpectedClaimRevision int64           `json:"expected_claim_revision,omitempty"`
	LeaseTTL              time.Duration   `json:"-"`
	PolicyDigest          string          `json:"policy_digest,omitempty"`
	Now                   time.Time       `json:"-"`
	Recovery              *RecoveryIntent `json:"recovery,omitempty"`
	Verified              *RecoveryProof  `json:"-"`
}

type RecoveryIntent struct {
	StopProofID          string           `json:"stop_proof_id"`
	TaskHeadDigest       string           `json:"task_head_digest"`
	ProcessProofDigest   string           `json:"process_proof_digest"`
	EffectEvidenceDigest string           `json:"effect_evidence_digest"`
	EffectResolution     EffectResolution `json:"effect_resolution"`
}

func (i RecoveryIntent) validate() error {
	if !validID(i.StopProofID) || !digest(i.TaskHeadDigest) || !digest(i.ProcessProofDigest) ||
		!digest(i.EffectEvidenceDigest) || i.EffectResolution != EffectFree && i.EffectResolution != ResolvedNoReplay {
		return fail(CodeUnsafeRecovery, "proof")
	}
	return nil
}

type ClaimRequest struct {
	BoardID, CardID, IdempotencyKey string
	ExpectedCardRevision            int64
}

type HeartbeatRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedClaimRevision                               int64
}

type RecoverClaimRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
	Proof                                               RecoveryIntent
}

type RecoveryRecord struct {
	Version              int              `json:"version"`
	ID                   string           `json:"id"`
	BoardID              string           `json:"board_id"`
	CardID               string           `json:"card_id"`
	AttemptID            string           `json:"attempt_id"`
	OldClaimID           string           `json:"old_claim_id"`
	OldClaimRevision     int64            `json:"old_claim_revision"`
	CardRevision         int64            `json:"card_revision"`
	StopProofID          string           `json:"stop_proof_id"`
	TaskHeadDigest       string           `json:"task_head_digest"`
	ProcessProofDigest   string           `json:"process_proof_digest"`
	EffectEvidenceDigest string           `json:"effect_evidence_digest"`
	EffectResolution     EffectResolution `json:"effect_resolution"`
	ResultingState       State            `json:"resulting_state"`
	FirstSequence        int64            `json:"first_sequence"`
	LastSequence         int64            `json:"last_sequence"`
	RecoveredAt          time.Time        `json:"recovered_at"`
}

func (r RecoveryRecord) Validate() error {
	if r.Version != SchemaVersion || !validID(r.ID) || !validLifecycleIDs(r.BoardID, r.CardID, r.AttemptID, r.OldClaimID, r.StopProofID) ||
		r.OldClaimRevision < 1 || r.CardRevision < 1 || !digest(r.TaskHeadDigest) || !digest(r.ProcessProofDigest) ||
		!digest(r.EffectEvidenceDigest) || r.EffectResolution != EffectFree && r.EffectResolution != ResolvedNoReplay ||
		r.ResultingState != Ready || r.FirstSequence < 1 || r.LastSequence < r.FirstSequence ||
		r.LastSequence-r.FirstSequence+1 > MaxTransactionEvents || !validTime(r.RecoveredAt) {
		return fail(CodeInvalid, "recovery_record")
	}
	return nil
}

type LifecycleRepository interface {
	ApplyLifecycleMutation(context.Context, LifecycleMutation) (OperationReceipt, error)
	ReplayLifecycleMutation(context.Context, LifecycleMutation) (OperationReceipt, bool, error)
}

// RecoveryVerifier turns externally inspectable durable observations into the
// two safety conclusions required by ApplyRecovery. Digests alone are never
// treated as proof that a task is terminal or a process has stopped.
type RecoveryVerifier interface {
	VerifyRecovery(context.Context, RecoverClaimRequest, Actor) (RecoveryProof, error)
}

type LifecycleService struct {
	repository   LifecycleRepository
	authority    AuthoritySource
	verifier     RecoveryVerifier
	now          func() time.Time
	leaseTTL     time.Duration
	policyDigest string
}

func NewLifecycleService(repository LifecycleRepository, authority AuthoritySource, verifier RecoveryVerifier,
	now func() time.Time, leaseTTL time.Duration, policyDigest string) (*LifecycleService, error) {
	if repository == nil || authority == nil || verifier == nil || now == nil || leaseTTL < MinLeaseTTL || leaseTTL > MaxLeaseTTL || !digest(policyDigest) {
		return nil, fail(CodeInvalid, "service")
	}
	return &LifecycleService{repository: repository, authority: authority, verifier: verifier, now: now, leaseTTL: leaseTTL, policyDigest: policyDigest}, nil
}

func (s *LifecycleService) Claim(ctx context.Context, request ClaimRequest) (OperationReceipt, error) {
	if !validID(request.BoardID) || !validID(request.CardID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 {
		return OperationReceipt{}, fail(CodeInvalid, "claim")
	}
	actor, err := s.authorize(ctx, true)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := LifecycleMutation{Version: LifecycleMutationVersion, Kind: LifecycleClaim, BoardID: request.BoardID, CardID: request.CardID,
		IdempotencyKey: request.IdempotencyKey, Actor: actor, ExpectedCardRevision: request.ExpectedCardRevision,
		LeaseTTL: s.leaseTTL, PolicyDigest: s.policyDigest, Now: s.now().UTC()}
	return s.execute(ctx, mutation)
}

func (s *LifecycleService) Heartbeat(ctx context.Context, request HeartbeatRequest) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) || request.ExpectedClaimRevision < 1 {
		return OperationReceipt{}, fail(CodeInvalid, "heartbeat")
	}
	actor, err := s.authorize(ctx, true)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := LifecycleMutation{Version: LifecycleMutationVersion, Kind: LifecycleHeartbeat, BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: actor,
		ExpectedClaimRevision: request.ExpectedClaimRevision, LeaseTTL: s.leaseTTL, Now: s.now().UTC()}
	return s.execute(ctx, mutation)
}

func (s *LifecycleService) Recover(ctx context.Context, request RecoverClaimRequest) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) ||
		request.ExpectedCardRevision < 1 || request.ExpectedClaimRevision < 1 || request.Proof.validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "recovery")
	}
	actor, err := s.authorize(ctx, false)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := LifecycleMutation{Version: LifecycleMutationVersion, Kind: LifecycleRecover, BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: actor,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision,
		Recovery: &request.Proof, Now: s.now().UTC()}
	if receipt, found, replayErr := s.replay(ctx, mutation); found || replayErr != nil {
		return receipt, replayErr
	}
	verified, err := s.verifier.VerifyRecovery(ctx, request, actor)
	if err != nil {
		return OperationReceipt{}, err
	}
	if verified.StopProofID != request.Proof.StopProofID || verified.TaskHeadDigest != request.Proof.TaskHeadDigest ||
		verified.ProcessProofDigest != request.Proof.ProcessProofDigest || verified.EffectEvidenceDigest != request.Proof.EffectEvidenceDigest ||
		verified.EffectResolution != request.Proof.EffectResolution || !verified.TaskTerminal || !verified.ProcessStopped {
		return OperationReceipt{}, fail(CodeUnsafeRecovery, "proof")
	}
	mutation.Verified = &verified
	return s.apply(ctx, mutation)
}

func (s *LifecycleService) execute(ctx context.Context, mutation LifecycleMutation) (OperationReceipt, error) {
	if receipt, found, err := s.replay(ctx, mutation); found || err != nil {
		return receipt, err
	}
	return s.apply(ctx, mutation)
}

func (s *LifecycleService) replay(ctx context.Context, mutation LifecycleMutation) (OperationReceipt, bool, error) {
	digestValue, err := LifecycleDigest(mutation)
	if err != nil {
		return OperationReceipt{}, false, err
	}
	mutation.RequestDigest = digestValue
	return s.repository.ReplayLifecycleMutation(ctx, mutation)
}

func (s *LifecycleService) apply(ctx context.Context, mutation LifecycleMutation) (OperationReceipt, error) {
	digestValue, err := LifecycleDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation.RequestDigest = digestValue
	receipt, err := s.repository.ApplyLifecycleMutation(ctx, mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt.Validate() != nil || receipt.BoardID != mutation.BoardID || receipt.CardID != mutation.CardID {
		return OperationReceipt{}, fail(CodeInvalid, "stored_receipt")
	}
	return receipt, nil
}

func (s *LifecycleService) authorize(ctx context.Context, worker bool) (Actor, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil {
		return Actor{}, err
	}
	if authority.Validate() != nil || worker && authority.Actor.Type != "worker" || !worker && authority.Actor.Type != "operator" && authority.Actor.Type != "system" {
		return Actor{}, fail(CodeInvalid, "authority")
	}
	return authority.Actor, nil
}

func LifecycleDigest(mutation LifecycleMutation) (string, error) {
	copy := mutation
	copy.IdempotencyKey, copy.RequestDigest, copy.Now, copy.LeaseTTL, copy.Verified = "", "", time.Time{}, 0, nil
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "mutation")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func validLifecycleIDs(ids ...string) bool {
	for _, id := range ids {
		if !validID(id) {
			return false
		}
	}
	return true
}
