package workboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const ControlMutationVersion = 1

type ControlKind string

const (
	ControlPauseRequest   ControlKind = "card.pause_request"
	ControlCancelRequest  ControlKind = "card.cancel_request"
	ControlCancelFinalize ControlKind = "card.cancel_finalize"
	ControlBlock          ControlKind = "card.block"
	ControlUnblock        ControlKind = "card.unblock"
)

type ControlMutation struct {
	Version               int             `json:"version"`
	Kind                  ControlKind     `json:"kind"`
	BoardID               string          `json:"board_id"`
	CardID                string          `json:"card_id"`
	AttemptID             string          `json:"attempt_id,omitempty"`
	ClaimID               string          `json:"claim_id,omitempty"`
	IdempotencyKey        string          `json:"-"`
	RequestDigest         string          `json:"-"`
	Actor                 Actor           `json:"actor"`
	ExpectedCardRevision  int64           `json:"expected_card_revision"`
	ExpectedClaimRevision int64           `json:"expected_claim_revision,omitempty"`
	ReasonCode            string          `json:"reason_code,omitempty"`
	Stop                  *RecoveryIntent `json:"stop,omitempty"`
	Verified              *RecoveryProof  `json:"-"`
	Now                   time.Time       `json:"-"`
}

type RequestCardControl struct {
	BoardID, CardID, IdempotencyKey string
	ExpectedCardRevision            int64
}

type ClaimCardControl struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
	ReasonCode                                          string
}

type FinalizeCancelRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
	Proof                                               RecoveryIntent
}

type ControlRepository interface {
	ApplyControlMutation(context.Context, ControlMutation) (OperationReceipt, error)
	ReplayControlMutation(context.Context, ControlMutation) (OperationReceipt, bool, error)
}

type ControlVerifier interface {
	VerifyControlStop(context.Context, FinalizeCancelRequest, Actor) (RecoveryProof, error)
}

type ControlService struct {
	repository ControlRepository
	authority  AuthoritySource
	verifier   ControlVerifier
	now        func() time.Time
}

func NewControlService(repository ControlRepository, authority AuthoritySource, verifier ControlVerifier, now func() time.Time) (*ControlService, error) {
	if repository == nil || authority == nil || verifier == nil || now == nil {
		return nil, fail(CodeInvalid, "service")
	}
	return &ControlService{repository: repository, authority: authority, verifier: verifier, now: now}, nil
}

func (s *ControlService) RequestPause(ctx context.Context, request RequestCardControl) (OperationReceipt, error) {
	return s.request(ctx, ControlPauseRequest, request)
}

func (s *ControlService) RequestCancel(ctx context.Context, request RequestCardControl) (OperationReceipt, error) {
	return s.request(ctx, ControlCancelRequest, request)
}

func (s *ControlService) request(ctx context.Context, kind ControlKind, request RequestCardControl) (OperationReceipt, error) {
	if !validID(request.BoardID) || !validID(request.CardID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 {
		return OperationReceipt{}, fail(CodeInvalid, "control")
	}
	actor, err := s.authorize(ctx, "operator")
	if err != nil {
		return OperationReceipt{}, err
	}
	return s.execute(ctx, ControlMutation{Version: 1, Kind: kind, BoardID: request.BoardID, CardID: request.CardID,
		IdempotencyKey: request.IdempotencyKey, Actor: actor, ExpectedCardRevision: request.ExpectedCardRevision, Now: s.now().UTC()})
}

func (s *ControlService) Block(ctx context.Context, request ClaimCardControl) (OperationReceipt, error) {
	return s.claimControl(ctx, ControlBlock, request)
}

func (s *ControlService) Unblock(ctx context.Context, request ClaimCardControl) (OperationReceipt, error) {
	return s.claimControl(ctx, ControlUnblock, request)
}

func (s *ControlService) claimControl(ctx context.Context, kind ControlKind, request ClaimCardControl) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) ||
		request.ExpectedCardRevision < 1 || request.ExpectedClaimRevision < 1 || !validID(request.ReasonCode) {
		return OperationReceipt{}, fail(CodeInvalid, "control")
	}
	actor, err := s.authorize(ctx, "worker")
	if err != nil {
		return OperationReceipt{}, err
	}
	return s.execute(ctx, ControlMutation{Version: 1, Kind: kind, BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: actor,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision,
		ReasonCode: request.ReasonCode, Now: s.now().UTC()})
}

func (s *ControlService) FinalizeCancel(ctx context.Context, request FinalizeCancelRequest) (OperationReceipt, error) {
	if !validLifecycleIDs(request.BoardID, request.CardID, request.AttemptID, request.ClaimID) || !validKey(request.IdempotencyKey) ||
		request.ExpectedCardRevision < 1 || request.ExpectedClaimRevision < 1 || request.Proof.validate() != nil {
		return OperationReceipt{}, fail(CodeInvalid, "cancel")
	}
	actor, err := s.authorize(ctx, "operator")
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := ControlMutation{Version: 1, Kind: ControlCancelFinalize, BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey, Actor: actor,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision,
		Stop: &request.Proof, Now: s.now().UTC()}
	if receipt, found, replayErr := s.replay(ctx, mutation); found || replayErr != nil {
		return receipt, replayErr
	}
	proof, err := s.verifier.VerifyControlStop(ctx, request, actor)
	if err != nil {
		return OperationReceipt{}, err
	}
	if !verifiedControlProof(request.Proof, proof) {
		return OperationReceipt{}, fail(CodeUnsafeRecovery, "proof")
	}
	mutation.Verified = &proof
	return s.apply(ctx, mutation)
}

func (s *ControlService) execute(ctx context.Context, mutation ControlMutation) (OperationReceipt, error) {
	if receipt, found, err := s.replay(ctx, mutation); found || err != nil {
		return receipt, err
	}
	return s.apply(ctx, mutation)
}

func (s *ControlService) replay(ctx context.Context, mutation ControlMutation) (OperationReceipt, bool, error) {
	digestValue, err := ControlDigest(mutation)
	if err != nil {
		return OperationReceipt{}, false, err
	}
	mutation.RequestDigest = digestValue
	return s.repository.ReplayControlMutation(ctx, mutation)
}

func (s *ControlService) apply(ctx context.Context, mutation ControlMutation) (OperationReceipt, error) {
	digestValue, err := ControlDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation.RequestDigest = digestValue
	receipt, err := s.repository.ApplyControlMutation(ctx, mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt.Validate() != nil || receipt.BoardID != mutation.BoardID || receipt.CardID != mutation.CardID {
		return OperationReceipt{}, fail(CodeInvalid, "stored_receipt")
	}
	return receipt, nil
}

func (s *ControlService) authorize(ctx context.Context, actorType string) (Actor, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil {
		return Actor{}, err
	}
	if authority.Validate() != nil || authority.Actor.Type != actorType {
		return Actor{}, fail(CodeInvalid, "authority")
	}
	return authority.Actor, nil
}

func ControlDigest(mutation ControlMutation) (string, error) {
	copy := mutation
	copy.IdempotencyKey, copy.RequestDigest, copy.Now, copy.Verified = "", "", time.Time{}, nil
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "mutation")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func verifiedControlProof(intent RecoveryIntent, proof RecoveryProof) bool {
	return proof.TaskTerminal && proof.ProcessStopped && proof.StopProofID == intent.StopProofID && proof.TaskHeadDigest == intent.TaskHeadDigest &&
		proof.ProcessProofDigest == intent.ProcessProofDigest && proof.EffectEvidenceDigest == intent.EffectEvidenceDigest && proof.EffectResolution == intent.EffectResolution
}
