package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// WorkboardClaimRecoveryRequest deliberately has no proof fields. It is an
// internal supervisor command, not a browser/model transport contract.
type WorkboardClaimRecoveryRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
}

// WorkboardCancelFinalizationRequest deliberately has no proof fields. Only a
// fixed trusted operator authority can execute it.
type WorkboardCancelFinalizationRequest struct {
	BoardID, CardID, AttemptID, ClaimID, IdempotencyKey string
	ExpectedCardRevision, ExpectedClaimRevision         int64
}

type fixedRecoveryAuthority struct{ authority workboard.Authority }

func (a fixedRecoveryAuthority) WorkboardAuthority(ctx context.Context) (workboard.Authority, error) {
	if ctx == nil || ctx.Err() != nil || a.authority.Validate() != nil {
		return workboard.Authority{}, ErrWorkboardRecoveryProof
	}
	return a.authority, nil
}

// WorkboardRecoveryExecutor composes proof preparation, exact replay, fresh
// verification, and transactional application behind proof-free trusted
// requests. A system actor may recover claims; cancel finalization requires an
// operator actor as enforced by ControlService.
type WorkboardRecoveryExecutor struct {
	store     *Store
	actor     workboard.Actor
	lifecycle *workboard.LifecycleService
	control   *workboard.ControlService
}

func NewWorkboardRecoveryExecutor(store *Store, actor workboard.Actor, now func() time.Time) (*WorkboardRecoveryExecutor, error) {
	if store == nil || actor.Validate() != nil || (actor.Type != "operator" && actor.Type != "system") || now == nil {
		return nil, ErrWorkboardRecoveryProof
	}
	authority := fixedRecoveryAuthority{workboard.Authority{CreationScope: "trusted-supervisor", Actor: actor}}
	lifecycle, err := workboard.NewLifecycleService(store, authority, store, now, time.Second, strings.Repeat("0", 64))
	if err != nil {
		return nil, err
	}
	control, err := workboard.NewControlService(store, authority, store, now)
	if err != nil {
		return nil, err
	}
	return &WorkboardRecoveryExecutor{store: store, actor: actor, lifecycle: lifecycle, control: control}, nil
}

func (e *WorkboardRecoveryExecutor) RecoverClaim(ctx context.Context, request WorkboardClaimRecoveryRequest) (workboard.OperationReceipt, error) {
	if e == nil || e.store == nil || e.lifecycle == nil {
		return workboard.OperationReceipt{}, ErrWorkboardRecoveryProof
	}
	target := WorkboardRecoveryTarget{request.BoardID, request.CardID, request.AttemptID, request.ClaimID}
	proof, err := e.store.PrepareWorkboardRecovery(ctx, target, e.actor)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	return e.lifecycle.Recover(ctx, workboard.RecoverClaimRequest{BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision, Proof: proof})
}

func (e *WorkboardRecoveryExecutor) FinalizeCancel(ctx context.Context, request WorkboardCancelFinalizationRequest) (workboard.OperationReceipt, error) {
	if e == nil || e.store == nil || e.control == nil || e.actor.Type != "operator" {
		return workboard.OperationReceipt{}, ErrWorkboardRecoveryProof
	}
	target := WorkboardRecoveryTarget{request.BoardID, request.CardID, request.AttemptID, request.ClaimID}
	proof, err := e.store.PrepareWorkboardRecovery(ctx, target, e.actor)
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	return e.control.FinalizeCancel(ctx, workboard.FinalizeCancelRequest{BoardID: request.BoardID, CardID: request.CardID,
		AttemptID: request.AttemptID, ClaimID: request.ClaimID, IdempotencyKey: request.IdempotencyKey,
		ExpectedCardRevision: request.ExpectedCardRevision, ExpectedClaimRevision: request.ExpectedClaimRevision, Proof: proof})
}

var _ workboard.RecoveryVerifier = (*Store)(nil)
var _ workboard.ControlVerifier = (*Store)(nil)
