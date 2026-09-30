package workboard

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

const TaskStartClaimVersion = 1

// TaskStartClaim is a trusted-host command, not a client or model surface. It
// binds the first immutable runtime fact to the worker's board ownership so a
// crash can expose both records or neither record.
type TaskStartClaim struct {
	Version     int                   `json:"version"`
	Event       runtime.Event         `json:"event"`
	Claim       LifecycleMutation     `json:"claim"`
	Reservation *ExecutionReservation `json:"reservation,omitempty"`
	// authorized can only be set by LifecycleService in this package. It keeps
	// the exported repository transport from becoming a way to forge Actor.
	authorized bool
}

// Validate checks the complete cross-domain identity before durable storage is
// entered. The generated attempt and claim IDs remain storage-owned.
func (r TaskStartClaim) Validate() error {
	e, claim := r.Event, r.Claim
	if !r.authorized || r.Version != TaskStartClaimVersion || e.Validate() != nil || e.Kind != runtime.TaskStarted || e.Sequence != 1 ||
		e.Data.SubmissionID != "" ||
		claim.Version != LifecycleMutationVersion || claim.Kind != LifecycleClaim || claim.Actor.Type != "worker" ||
		e.WorkerID == "" || e.WorkerID != claim.Actor.ID || e.TaskID != claim.TaskID || e.SessionID != claim.SessionID ||
		!e.Time.Equal(claim.Now) || claim.Now.Location() != time.UTC || claim.ExpectedCardRevision < 1 ||
		claim.ExpectedClaimRevision != 0 || claim.AttemptID != "" || claim.ClaimID != "" ||
		claim.LeaseTTL < MinLeaseTTL || claim.LeaseTTL > MaxLeaseTTL || !digest(claim.PolicyDigest) ||
		!validID(claim.BoardID) || !validID(claim.CardID) || !validKey(claim.IdempotencyKey) || claim.Recovery != nil ||
		claim.EffectResolution != "" {
		return fail(CodeInvalid, "task_start_claim")
	}
	want, err := LifecycleDigest(claim)
	if err != nil || claim.RequestDigest != want {
		return fail(CodeInvalid, "task_start_claim_digest")
	}
	if r.Reservation != nil && r.Reservation.Validate(e) != nil {
		return fail(CodeInvalid, "task_start_claim_reservation")
	}
	return nil
}

// TaskStartClaimRepository is deliberately separate from LifecycleRepository:
// only a runtime host that already owns task identity should receive it.
type TaskStartClaimRepository interface {
	CommitTaskStartClaim(context.Context, TaskStartClaim) (OperationReceipt, error)
}

// TaskStartClaimRecord is the immutable cross-domain commit marker. The
// normalized storage columns are accelerators; this canonical record remains
// the authority used to reject partial or independently committed halves.
type TaskStartClaimRecord struct {
	Version              int       `json:"version"`
	TaskID               string    `json:"task_id"`
	SessionID            string    `json:"session_id"`
	EventID              string    `json:"event_id"`
	EventDigest          string    `json:"event_digest"`
	BoardID              string    `json:"board_id"`
	CardID               string    `json:"card_id"`
	AttemptID            string    `json:"attempt_id"`
	ClaimID              string    `json:"claim_id"`
	WorkerID             string    `json:"worker_id"`
	OperationID          string    `json:"operation_id"`
	RequestDigest        string    `json:"request_digest"`
	ExpectedCardRevision int64     `json:"expected_card_revision"`
	PolicyDigest         string    `json:"policy_digest"`
	LeaseTTLNS           int64     `json:"lease_ttl_ns"`
	CreatedAt            time.Time `json:"created_at"`
}

func (r TaskStartClaimRecord) Validate() error {
	if r.Version != SchemaVersion || !validLifecycleIDs(r.TaskID, r.SessionID, r.EventID, r.BoardID, r.CardID, r.AttemptID, r.ClaimID, r.WorkerID, r.OperationID) ||
		!digest(r.EventDigest) || !digest(r.RequestDigest) || !digest(r.PolicyDigest) || r.ExpectedCardRevision < 1 ||
		time.Duration(r.LeaseTTLNS) < MinLeaseTTL || time.Duration(r.LeaseTTLNS) > MaxLeaseTTL || !validTime(r.CreatedAt) {
		return fail(CodeInvalid, "task_start_claim_record")
	}
	return nil
}

// ClaimTaskStart is the only application-facing constructor for the composite
// command. Authority, policy, lease time and request digest remain owned by the
// configured LifecycleService rather than a browser, model, or callback.
func (s *LifecycleService) ClaimTaskStart(ctx context.Context, event runtime.Event, request ClaimRequest) (OperationReceipt, error) {
	return s.claimTaskStart(ctx, event, request, nil)
}

// ClaimBudgetedTaskStart is the production admission boundary. Unlike the
// legacy method used by tests and manually composed hosts, it requires the
// store to reserve Workboard WIP and aggregate card capacity in the same
// transaction as TaskStarted and the worker claim.
func (s *LifecycleService) ClaimBudgetedTaskStart(ctx context.Context, event runtime.Event, request ClaimRequest, reservation ExecutionReservation) (OperationReceipt, error) {
	if reservation.Validate(event) != nil {
		return OperationReceipt{}, fail(CodeInvalid, "task_start_claim_reservation")
	}
	copy := reservation
	return s.claimTaskStart(ctx, event, request, &copy)
}

func (s *LifecycleService) claimTaskStart(ctx context.Context, event runtime.Event, request ClaimRequest, reservation *ExecutionReservation) (OperationReceipt, error) {
	if s == nil || ctx == nil || event.Validate() != nil || event.Kind != runtime.TaskStarted || event.Sequence != 1 || event.Data.SubmissionID != "" ||
		!validID(request.BoardID) || !validID(request.CardID) || !validKey(request.IdempotencyKey) || request.ExpectedCardRevision < 1 ||
		request.TaskID != event.TaskID || request.SessionID != event.SessionID {
		return OperationReceipt{}, fail(CodeInvalid, "task_start_claim")
	}
	repository, ok := s.repository.(TaskStartClaimRepository)
	if !ok {
		return OperationReceipt{}, fail(CodeInvalid, "task_start_claim_repository")
	}
	actor, err := s.authorize(ctx, true)
	if err != nil {
		return OperationReceipt{}, err
	}
	mutation := LifecycleMutation{Version: LifecycleMutationVersion, Kind: LifecycleClaim, BoardID: request.BoardID, CardID: request.CardID,
		IdempotencyKey: request.IdempotencyKey, Actor: actor, ExpectedCardRevision: request.ExpectedCardRevision,
		LeaseTTL: s.leaseTTL, PolicyDigest: s.policyDigest, TaskID: request.TaskID, SessionID: request.SessionID, Now: event.Time.UTC()}
	mutation.RequestDigest, err = LifecycleDigest(mutation)
	if err != nil {
		return OperationReceipt{}, err
	}
	command := TaskStartClaim{Version: TaskStartClaimVersion, Event: event, Claim: mutation, Reservation: reservation, authorized: true}
	if err = command.Validate(); err != nil {
		return OperationReceipt{}, err
	}
	receipt, err := repository.CommitTaskStartClaim(ctx, command)
	if err != nil {
		return OperationReceipt{}, err
	}
	if receipt.Validate() != nil || receipt.BoardID != request.BoardID || receipt.CardID != request.CardID || receipt.RequestDigest != mutation.RequestDigest {
		return OperationReceipt{}, fail(CodeInvalid, "stored_receipt")
	}
	return receipt, nil
}
