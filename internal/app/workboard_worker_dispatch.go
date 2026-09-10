package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// WorkboardWorkerDispatch is an internal capability-bound entry point for the
// worker-owned lifecycle operations currently implemented by the durable
// workboard store. It deliberately exposes neither recovery nor operator
// commands, and it never derives worker identity from a transport request.
type WorkboardWorkerDispatch struct {
	lifecycle  *workboard.LifecycleService
	progress   *workboard.ProgressService
	control    *workboard.ControlService
	evaluation *workboard.EvaluationService
}

type fixedWorkboardAuthority struct{ authority workboard.Authority }

func (a fixedWorkboardAuthority) WorkboardAuthority(ctx context.Context) (workboard.Authority, error) {
	if ctx == nil || ctx.Err() != nil || a.authority.Validate() != nil {
		return workboard.Authority{}, errors.New("workboard worker authority unavailable")
	}
	return a.authority, nil
}

type unavailableRecoveryVerifier struct{}

func (unavailableRecoveryVerifier) VerifyRecovery(context.Context, workboard.RecoverClaimRequest, workboard.Actor) (workboard.RecoveryProof, error) {
	return workboard.RecoveryProof{}, errors.New("workboard recovery unavailable through worker dispatch")
}

func NewWorkboardWorkerDispatch(repository interface {
	workboard.LifecycleRepository
	workboard.ProgressRepository
	workboard.ControlRepository
	workboard.EvaluationRepository
}, workerID, policyDigest string, leaseTTL time.Duration, evaluator workboard.CandidateEvaluator, now func() time.Time) (*WorkboardWorkerDispatch, error) {
	authority := fixedWorkboardAuthority{authority: workboard.Authority{CreationScope: "internal-worker", Actor: workboard.Actor{ID: workerID, Type: "worker"}}}
	lifecycle, err := workboard.NewLifecycleService(repository, authority, unavailableRecoveryVerifier{}, now, leaseTTL, policyDigest)
	if err != nil {
		return nil, err
	}
	progress, err := workboard.NewProgressService(repository, authority, now)
	if err != nil {
		return nil, err
	}
	control, err := workboard.NewControlService(repository, authority, unavailableControlVerifier{}, now)
	if err != nil {
		return nil, err
	}
	evaluation, err := workboard.NewEvaluationService(repository, authority, evaluator, now)
	if err != nil {
		return nil, err
	}
	return &WorkboardWorkerDispatch{lifecycle: lifecycle, progress: progress, control: control, evaluation: evaluation}, nil
}

func (d *WorkboardWorkerDispatch) Claim(ctx context.Context, request workboard.ClaimRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.lifecycle == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	if request.TaskID == "" || request.SessionID == "" {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.lifecycle.Claim(ctx, request)
}

// ClaimTaskStart atomically commits the first runtime event with the card
// claim. Callers must pass the already-redacted event that the runtime host is
// about to acknowledge; independently appending TaskStarted and then claiming
// would leave a crash window with only one half visible.
func (d *WorkboardWorkerDispatch) ClaimTaskStart(ctx context.Context, event runtime.Event, request workboard.ClaimRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.lifecycle == nil || ctx == nil || event.Validate() != nil || event.Kind != runtime.TaskStarted || event.Sequence != 1 {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	if request.TaskID == "" || request.SessionID == "" {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.lifecycle.ClaimTaskStart(ctx, event, request)
}

// ClaimBudgetedTaskStart is the stock scheduler path. Capacity admission is
// inseparable from the first runtime event and worker claim; callers cannot
// reserve capacity speculatively or after provider execution has begun.
func (d *WorkboardWorkerDispatch) ClaimBudgetedTaskStart(ctx context.Context, event runtime.Event, request workboard.ClaimRequest,
	reservation workboard.ExecutionReservation,
) (workboard.OperationReceipt, error) {
	if d == nil || d.lifecycle == nil || ctx == nil || reservation.Validate(event) != nil ||
		event.Validate() != nil || event.Kind != runtime.TaskStarted || event.Sequence != 1 ||
		request.TaskID == "" || request.SessionID == "" {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.lifecycle.ClaimBudgetedTaskStart(ctx, event, request, reservation)
}

func (d *WorkboardWorkerDispatch) Heartbeat(ctx context.Context, request workboard.HeartbeatRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.lifecycle == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.lifecycle.Heartbeat(ctx, request)
}

func (d *WorkboardWorkerDispatch) Fail(ctx context.Context, request workboard.FailClaimRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.lifecycle == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.lifecycle.Fail(ctx, request)
}

func (d *WorkboardWorkerDispatch) AppendCheckpoint(ctx context.Context, request workboard.AppendCheckpointRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.progress == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.progress.AppendCheckpoint(ctx, request)
}

func (d *WorkboardWorkerDispatch) SubmitCandidate(ctx context.Context, request workboard.SubmitCandidateRequest) (workboard.OperationReceipt, error) {
	if d == nil || d.evaluation == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.evaluation.SubmitCandidate(ctx, request)
}

func (d *WorkboardWorkerDispatch) Block(ctx context.Context, request workboard.ClaimCardControl) (workboard.OperationReceipt, error) {
	if d == nil || d.control == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.control.Block(ctx, request)
}

func (d *WorkboardWorkerDispatch) Unblock(ctx context.Context, request workboard.ClaimCardControl) (workboard.OperationReceipt, error) {
	if d == nil || d.control == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.control.Unblock(ctx, request)
}

func (d *WorkboardWorkerDispatch) AcknowledgePause(ctx context.Context, request workboard.ClaimPauseControl) (workboard.OperationReceipt, error) {
	if d == nil || d.control == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.control.AcknowledgePause(ctx, request)
}

func (d *WorkboardWorkerDispatch) AcknowledgeResume(ctx context.Context, request workboard.ClaimPauseControl) (workboard.OperationReceipt, error) {
	if d == nil || d.control == nil {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	return d.control.AcknowledgeResume(ctx, request)
}
