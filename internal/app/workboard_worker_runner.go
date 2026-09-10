package app

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

// WorkboardCandidate is the bounded output that enters durable review. The
// runner validates it before submission; acceptance remains a separate trusted
// operator/validator decision.
type WorkboardCandidate struct {
	Summary      string
	ArtifactRefs []string
}

// WorkboardWorkerTask binds one already-identified runtime task to one ready
// card. Runtime identity is host configuration, never model/browser input.
type WorkboardWorkerTask struct {
	BoardID, CardID, TaskID, SessionID, ParentTaskID, Scope string
	SubmissionID                                            string
	ExpectedCardRevision                                    int64
	// FailureEffect is a trusted host classification of the task's possible
	// side effects. Only NoEffect permits automatic release after failure.
	FailureEffect runtime.Effect
	Execute       func(context.Context, *WorkboardWorkerHandle) (WorkboardCandidate, error)
	Validate      func(context.Context, WorkboardCandidate) error
}

type workboardWorkerRepository interface {
	workboard.LifecycleRepository
	workboard.ProgressRepository
	workboard.ControlRepository
	workboard.EvaluationRepository
	workboard.LifecycleSnapshotRepository
	workboard.WorkerControlRepository
}

// WorkboardWorkerRunner composes the existing bounded in-process supervisor
// with the durable workboard protocol. It does not poll, reassign, recover, or
// retry: any ambiguous boundary remains represented by the durable claim and
// runtime event history for independent supervision.
type WorkboardWorkerRunner struct {
	supervisor *workers.Supervisor
	repository workboardWorkerRepository
	evaluator  workboard.CandidateEvaluator
	policy     string
	heartbeat  time.Duration
	ttl        time.Duration
	now        func() time.Time
}

func NewWorkboardWorkerRunner(supervisor *workers.Supervisor, repository workboardWorkerRepository,
	evaluator workboard.CandidateEvaluator, policyDigest string, heartbeat, ttl time.Duration, now func() time.Time,
) (*WorkboardWorkerRunner, error) {
	if supervisor == nil || repository == nil || evaluator == nil || now == nil || heartbeat < time.Millisecond ||
		ttl <= 2*heartbeat || ttl > workboard.MaxLeaseTTL || len(policyDigest) != 64 {
		return nil, ErrAdmission
	}
	return &WorkboardWorkerRunner{supervisor: supervisor, repository: repository, evaluator: evaluator,
		policy: policyDigest, heartbeat: heartbeat, ttl: ttl, now: now}, nil
}

func (r *WorkboardWorkerRunner) Run(ctx context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	if ctx == nil || r == nil || r.supervisor == nil || task.BoardID == "" || task.CardID == "" || task.TaskID == "" ||
		task.SessionID == "" || task.ParentTaskID == "" || task.Scope == "" || task.ExpectedCardRevision < 1 ||
		task.Execute == nil || task.Validate == nil ||
		(task.FailureEffect != runtime.NoEffect && task.FailureEffect != runtime.ConfirmedEffect && task.FailureEffect != runtime.UncertainEffect) {
		return WorkboardCandidate{}, ErrAdmission
	}
	workerID := rand.Text()
	dispatch, err := NewWorkboardWorkerDispatch(r.repository, workerID, r.policy, r.ttl, r.evaluator, r.now)
	if err != nil {
		return WorkboardCandidate{}, err
	}
	var candidate WorkboardCandidate
	var executionErr error
	supervised, stopSupervisor := context.WithCancel(ctx)
	defer stopSupervisor()
	output, err := r.supervisor.Run(supervised, workers.Work{TaskID: task.TaskID, SessionID: task.SessionID,
		ParentID: task.ParentTaskID, Scope: task.Scope, SubmissionID: task.SubmissionID, WorkerID: workerID,
		Execute: func(run context.Context) (string, error) {
			candidate, executionErr = r.execute(run, stopSupervisor, dispatch, workerID, task)
			return candidate.Summary, executionErr
		},
		// Validation already ran while the workboard lease was held and before
		// candidate submission. The supervisor still enforces its acceptance
		// boundary and durable terminal ordering around that result.
		Validate: func(context.Context, string) error { return nil },
	})
	if err != nil {
		return WorkboardCandidate{}, errors.Join(err, executionErr)
	}
	if output != candidate.Summary {
		return WorkboardCandidate{}, workers.ErrDurability
	}
	candidate.ArtifactRefs = append([]string{}, candidate.ArtifactRefs...)
	return candidate, nil
}

func (r *WorkboardWorkerRunner) execute(ctx context.Context, stopSupervisor context.CancelFunc, dispatch *WorkboardWorkerDispatch, workerID string,
	task WorkboardWorkerTask,
) (candidate WorkboardCandidate, runErr error) {
	claim, err := dispatch.Claim(ctx, workboard.ClaimRequest{BoardID: task.BoardID, CardID: task.CardID,
		IdempotencyKey: workboardOperationKey("claim"), ExpectedCardRevision: task.ExpectedCardRevision,
		TaskID: task.TaskID, SessionID: task.SessionID})
	if err != nil || claim.CardRevision == nil || claim.ClaimRevision == nil {
		return WorkboardCandidate{}, errors.Join(workers.ErrWork, err)
	}
	snapshots, err := r.repository.ReadCardLifecycleSnapshots(ctx, task.BoardID, []string{task.CardID})
	snapshot, found := snapshots[task.CardID]
	if err != nil || !found || snapshot.Attempt == nil || snapshot.Attempt.Claim == nil {
		return WorkboardCandidate{}, errors.Join(workers.ErrDurability, err)
	}
	attempt, durableClaim := snapshot.Attempt, snapshot.Attempt.Claim
	if attempt.WorkerID != workerID || durableClaim.OwnerID != workerID || durableClaim.TaskID != task.TaskID ||
		!containsExact(attempt.TaskIDs, task.TaskID) || !containsExact(attempt.SessionIDs, task.SessionID) ||
		durableClaim.Revision != *claim.ClaimRevision {
		return WorkboardCandidate{}, workers.ErrDurability
	}
	handle := &WorkboardWorkerHandle{dispatch: dispatch, controls: r.repository, boardID: task.BoardID, cardID: task.CardID,
		attemptID: attempt.ID, claimID: durableClaim.ID, cardRevision: *claim.CardRevision,
		claimRevision: *claim.ClaimRevision, criteriaRevision: attempt.CriteriaRevision, workerID: workerID,
		taskID: task.TaskID, active: true, controlChanged: make(chan struct{}, 1)}
	defer handle.revoke()
	run, cancel := context.WithCancel(ctx)
	handle.bindCancellation(cancel, stopSupervisor)
	heartbeats := make(chan error, 1)
	go handle.heartbeat(run, r.heartbeat, heartbeats)
	joined := false
	joinHeartbeat := func() error {
		if joined {
			return nil
		}
		cancel()
		joined = true
		return <-heartbeats
	}
	// This defer is also the panic boundary's join guarantee: Supervisor.Run
	// cannot return and release ownership while the board heartbeat still runs.
	defer func() { runErr = errors.Join(runErr, joinHeartbeat()) }()
	candidate, runErr = executeWorkboardCallback(run, handle, task)
	if runErr == nil {
		// Candidate release is a host-owned safe boundary too. A pause
		// committed after validation must be acknowledged before review state
		// can become visible, while the independent heartbeat remains live.
		runErr = handle.SafeBoundary(run)
	}
	heartbeatErr := joinHeartbeat()
	runErr = errors.Join(runErr, heartbeatErr, ctx.Err())
	if runErr != nil {
		if heartbeatErr == nil {
			cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cleanupCancel()
			if task.FailureEffect == runtime.NoEffect {
				runErr = errors.Join(runErr, handle.fail(cleanup))
			} else {
				runErr = errors.Join(runErr, handle.ensureBlocked(cleanup, "effect-review-required"))
			}
		}
		return WorkboardCandidate{}, runErr
	}
	if _, runErr = handle.submit(ctx, candidate); runErr != nil {
		return WorkboardCandidate{}, runErr
	}
	return candidate, nil
}

func executeWorkboardCallback(ctx context.Context, handle *WorkboardWorkerHandle, task WorkboardWorkerTask) (candidate WorkboardCandidate, err error) {
	defer func() {
		if recover() != nil {
			candidate = WorkboardCandidate{}
			err = workers.ErrWork
		}
	}()
	candidate, err = task.Execute(ctx, handle)
	if err == nil {
		if err = handle.SafeBoundary(ctx); err == nil {
			err = task.Validate(ctx, candidate)
		}
	}
	return candidate, err
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func workboardOperationKey(kind string) string { return "worker-" + kind + "-" + rand.Text() }

// WorkboardWorkerHandle exposes only operations owned by the fixed worker
// capability. Revision fences are maintained internally and serialized with
// background heartbeat writes.
type WorkboardWorkerHandle struct {
	mu                                            sync.Mutex
	dispatch                                      *WorkboardWorkerDispatch
	controls                                      workboard.WorkerControlRepository
	boardID, cardID, attemptID, claimID           string
	workerID, taskID                              string
	cardRevision, claimRevision, criteriaRevision int64
	active                                        bool
	blocked                                       bool
	paused                                        bool
	controlChanged                                chan struct{}
	cancelCallback, stopSupervisor                context.CancelFunc
}

// SafeBoundary is the only point at which a cooperative callback acknowledges
// a durable pause request. It keeps the claim and supervisor slot while paused;
// the independent heartbeat continues renewing the lease and observing an
// exact-fenced resume or cancellation. All other handle capabilities are
// denied between pause acknowledgement and resume acknowledgement.
func (h *WorkboardWorkerHandle) SafeBoundary(ctx context.Context) error {
	if ctx == nil {
		return ErrAdmission
	}
	for {
		h.mu.Lock()
		if !h.active {
			err := ctx.Err()
			h.mu.Unlock()
			if err != nil {
				return err
			}
			return ErrAdmission
		}
		// Refreshing through the worker-owned heartbeat reconciles a card
		// revision advanced by an operator control request while preserving
		// the exact attempt/claim/owner fence used by the observation.
		observed, err := h.refreshControlLocked(ctx)
		if err != nil {
			h.revokeAndCancelLocked()
			h.mu.Unlock()
			return err
		}
		if observed.CancelRequested {
			h.revokeAndCancelLocked()
			h.mu.Unlock()
			return context.Canceled
		}
		switch observed.PausePhase {
		case workboard.PauseNone:
			h.paused = false
			h.mu.Unlock()
			return nil
		case workboard.PauseRequested:
			err = h.acknowledgePauseLocked(ctx, true)
			if err == nil {
				h.paused = true
			}
		case workboard.PauseAcknowledged:
			h.paused = true
		case workboard.ResumeRequested:
			h.paused = true
			err = h.acknowledgePauseLocked(ctx, false)
			if err == nil {
				h.paused = false
				h.mu.Unlock()
				return nil
			}
		}
		if err != nil {
			// A cancellation may have committed concurrently with the worker's
			// pause/resume acknowledgement. Reconcile once through a fresh
			// heartbeat so cancellation wins over a stale phase error without
			// weakening any identity or revision fence.
			if after, observeErr := h.refreshControlLocked(ctx); observeErr == nil && after.CancelRequested {
				h.revokeAndCancelLocked()
				h.mu.Unlock()
				return context.Canceled
			}
			h.revokeAndCancelLocked()
			h.mu.Unlock()
			return err
		}
		changed := h.controlChanged
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (h *WorkboardWorkerHandle) refreshControlLocked(ctx context.Context) (workboard.WorkerControlObservation, error) {
	var observed workboard.WorkerControlObservation
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		receipt, heartbeatErr := h.dispatch.Heartbeat(ctx, workboard.HeartbeatRequest{BoardID: h.boardID, CardID: h.cardID,
			AttemptID: h.attemptID, ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("boundary-heartbeat"),
			ExpectedClaimRevision: h.claimRevision})
		if err = h.advance(receipt, heartbeatErr); err != nil {
			return workboard.WorkerControlObservation{}, err
		}
		if observed, err = h.readControlLocked(ctx); err == nil {
			return observed, nil
		}
		// An operator mutation can advance only the card revision between the
		// heartbeat transaction and the read snapshot. One more exact-fenced
		// heartbeat reconciles that benign race; replacement ownership still
		// fails at the heartbeat claim fence.
	}
	return workboard.WorkerControlObservation{}, err
}

func (h *WorkboardWorkerHandle) acknowledgePauseLocked(ctx context.Context, pause bool) error {
	request := workboard.ClaimPauseControl{BoardID: h.boardID, CardID: h.cardID, AttemptID: h.attemptID,
		ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("pause-control"),
		ExpectedCardRevision: h.cardRevision, ExpectedClaimRevision: h.claimRevision}
	var receipt workboard.OperationReceipt
	var err error
	if pause {
		receipt, err = h.dispatch.AcknowledgePause(ctx, request)
	} else {
		receipt, err = h.dispatch.AcknowledgeResume(ctx, request)
	}
	return h.advance(receipt, err)
}

func (h *WorkboardWorkerHandle) readControlLocked(ctx context.Context) (workboard.WorkerControlObservation, error) {
	target := workboard.WorkerControlTarget{BoardID: h.boardID, CardID: h.cardID, AttemptID: h.attemptID,
		ClaimID: h.claimID, WorkerID: h.workerID, TaskID: h.taskID,
		CardRevision: h.cardRevision, ClaimRevision: h.claimRevision}
	observed, err := h.controls.ReadWorkerControl(ctx, target)
	if err == nil && observed.Validate(target) != nil {
		err = workers.ErrDurability
	}
	return observed, err
}

func (h *WorkboardWorkerHandle) bindCancellation(cancelCallback, stopSupervisor context.CancelFunc) {
	h.mu.Lock()
	h.cancelCallback, h.stopSupervisor = cancelCallback, stopSupervisor
	h.mu.Unlock()
}

func (h *WorkboardWorkerHandle) revokeAndCancelLocked() {
	h.active = false
	h.paused = false
	if h.stopSupervisor != nil {
		h.stopSupervisor()
	}
	if h.cancelCallback != nil {
		h.cancelCallback()
	}
	h.signalControlLocked()
}

func (h *WorkboardWorkerHandle) signalControlLocked() {
	select {
	case h.controlChanged <- struct{}{}:
	default:
	}
}

func (h *WorkboardWorkerHandle) AppendCheckpoint(ctx context.Context, evidence string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || h.paused {
		return ErrAdmission
	}
	receipt, err := h.dispatch.AppendCheckpoint(ctx, workboard.AppendCheckpointRequest{BoardID: h.boardID, CardID: h.cardID,
		AttemptID: h.attemptID, ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("checkpoint"),
		ExpectedCardRevision: h.cardRevision, ExpectedClaimRevision: h.claimRevision,
		CriteriaRevision: h.criteriaRevision, Evidence: evidence})
	return h.advance(receipt, err)
}

func (h *WorkboardWorkerHandle) Block(ctx context.Context, reason string) error {
	return h.control(ctx, true, reason)
}

func (h *WorkboardWorkerHandle) Unblock(ctx context.Context, reason string) error {
	return h.control(ctx, false, reason)
}

func (h *WorkboardWorkerHandle) control(ctx context.Context, block bool, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || h.paused {
		return ErrAdmission
	}
	request := workboard.ClaimCardControl{BoardID: h.boardID, CardID: h.cardID, AttemptID: h.attemptID,
		ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("control"), ExpectedCardRevision: h.cardRevision,
		ExpectedClaimRevision: h.claimRevision, ReasonCode: reason}
	var receipt workboard.OperationReceipt
	var err error
	if block {
		receipt, err = h.dispatch.Block(ctx, request)
	} else {
		receipt, err = h.dispatch.Unblock(ctx, request)
	}
	if err = h.advance(receipt, err); err == nil {
		h.blocked = block
	}
	return err
}

func (h *WorkboardWorkerHandle) ensureBlocked(ctx context.Context, reason string) error {
	h.mu.Lock()
	if !h.active || h.paused {
		h.mu.Unlock()
		return ErrAdmission
	}
	if h.blocked {
		h.mu.Unlock()
		return nil
	}
	request := workboard.ClaimCardControl{BoardID: h.boardID, CardID: h.cardID, AttemptID: h.attemptID,
		ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("effect-block"), ExpectedCardRevision: h.cardRevision,
		ExpectedClaimRevision: h.claimRevision, ReasonCode: reason}
	receipt, err := h.dispatch.Block(ctx, request)
	if err = h.advance(receipt, err); err == nil {
		h.blocked = true
	}
	h.mu.Unlock()
	return err
}

func (h *WorkboardWorkerHandle) fail(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || h.paused {
		return ErrAdmission
	}
	receipt, err := h.dispatch.Fail(ctx, workboard.FailClaimRequest{BoardID: h.boardID, CardID: h.cardID,
		AttemptID: h.attemptID, ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("fail"),
		ExpectedCardRevision: h.cardRevision, ExpectedClaimRevision: h.claimRevision, EffectResolution: workboard.EffectFree})
	if err = h.advance(receipt, err); err != nil {
		return err
	}
	h.active = false
	return nil
}

func (h *WorkboardWorkerHandle) heartbeat(ctx context.Context, interval time.Duration, done chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			done <- nil
			return
		}
		h.mu.Lock()
		if !h.active {
			h.mu.Unlock()
			done <- nil
			return
		}
		observed, err := h.refreshControlLocked(ctx)
		if err == nil {
			if observed.CancelRequested {
				// Revoke the board mutation capability before waking callback
				// cleanup. A callback may use a fresh context while unwinding;
				// cancellation must not leave that escaped capability usable.
				h.revokeAndCancelLocked()
				h.mu.Unlock()
				done <- context.Canceled
				return
			}
			h.paused = observed.PausePhase == workboard.PauseAcknowledged || observed.PausePhase == workboard.ResumeRequested
			h.signalControlLocked()
		}
		if err != nil {
			// execute joins this goroutine by canceling ctx. If cancellation
			// arrives after a heartbeat dispatch but before its observation
			// finishes, that is an orderly join rather than evidence that the
			// durable claim fence was lost.
			if ctx.Err() != nil {
				h.mu.Unlock()
				done <- nil
				return
			}
			// Any loss of the durable heartbeat/control fence revokes this
			// capability before callback cleanup is awakened. A callback may
			// replace the canceled context while unwinding, but it must not be
			// able to mutate a claim whose ownership can no longer be proven.
			h.revokeAndCancelLocked()
		}
		h.mu.Unlock()
		if err != nil {
			done <- err
			return
		}
	}
}

func (h *WorkboardWorkerHandle) submit(ctx context.Context, candidate WorkboardCandidate) (workboard.OperationReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || h.paused {
		return workboard.OperationReceipt{}, ErrAdmission
	}
	receipt, err := h.dispatch.SubmitCandidate(ctx, workboard.SubmitCandidateRequest{BoardID: h.boardID, CardID: h.cardID,
		AttemptID: h.attemptID, ClaimID: h.claimID, IdempotencyKey: workboardOperationKey("candidate"),
		ExpectedCardRevision: h.cardRevision, ExpectedClaimRevision: h.claimRevision,
		CriteriaRevision: h.criteriaRevision, Summary: candidate.Summary, ArtifactRefs: append([]string{}, candidate.ArtifactRefs...)})
	if err != nil {
		return workboard.OperationReceipt{}, err
	}
	if err = h.advance(receipt, nil); err != nil {
		return workboard.OperationReceipt{}, err
	}
	h.active = false
	return receipt, nil
}

func (h *WorkboardWorkerHandle) advance(receipt workboard.OperationReceipt, err error) error {
	if err != nil {
		return err
	}
	if receipt.Validate() != nil || receipt.BoardID != h.boardID || receipt.CardID != h.cardID || receipt.CardRevision == nil {
		return workers.ErrDurability
	}
	h.cardRevision = *receipt.CardRevision
	if receipt.ClaimRevision != nil {
		h.claimRevision = *receipt.ClaimRevision
	}
	return nil
}

func (h *WorkboardWorkerHandle) revoke() {
	h.mu.Lock()
	h.active = false
	h.paused = false
	h.signalControlLocked()
	h.mu.Unlock()
}
