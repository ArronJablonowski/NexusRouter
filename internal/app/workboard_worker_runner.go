package app

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
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
// Scope remains a required reserved scheduler input, but this runner uses
// Supervisor.WithSlot and acquires no synthetic runtime resource lease for it.
type WorkboardWorkerTask struct {
	BoardID, CardID, TaskID, SessionID, ParentTaskID, Scope string
	// WorkerID is a trusted, stable scheduler identity. It is required to
	// exactly match an assigned card. Unassigned cards receive a fresh identity
	// when this field is empty.
	WorkerID             string
	SubmissionID         string
	ExpectedCardRevision int64
	// Reservation opts this task into the transactional schema-42 admission
	// path. The value is copied before the callback starts and must match the
	// actual task.started route exactly.
	Reservation *workboard.ExecutionReservation
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
	GetCard(context.Context, string, string) (workboard.Card, error)
	RuntimeStore() *telemetry.Store
}

// WorkboardWorkerRunner composes the existing bounded in-process supervisor
// with the durable workboard protocol. It does not poll, reassign, recover, or
// retry: any ambiguous boundary remains represented by the durable claim and
// runtime event history for independent supervision.
type WorkboardWorkerRunner struct {
	supervisor   *workers.Supervisor
	repository   workboardWorkerRepository
	evaluator    workboard.CandidateEvaluator
	policy       string
	heartbeat    time.Duration
	ttl          time.Duration
	now          func() time.Time
	runtimeStore *telemetry.Store
}

func NewWorkboardWorkerRunner(supervisor *workers.Supervisor, repository workboardWorkerRepository,
	evaluator workboard.CandidateEvaluator, policyDigest string, heartbeat, ttl time.Duration, now func() time.Time,
) (*WorkboardWorkerRunner, error) {
	if supervisor == nil || repository == nil || evaluator == nil || now == nil || heartbeat < time.Millisecond ||
		ttl <= 2*heartbeat || ttl > workboard.MaxLeaseTTL || len(policyDigest) != 64 {
		return nil, ErrAdmission
	}
	runtimeStore := repository.RuntimeStore()
	if runtimeStore == nil {
		return nil, ErrAdmission
	}
	return &WorkboardWorkerRunner{supervisor: supervisor, repository: repository, evaluator: evaluator,
		policy: policyDigest, heartbeat: heartbeat, ttl: ttl, now: now, runtimeStore: runtimeStore}, nil
}

func (r *WorkboardWorkerRunner) Run(ctx context.Context, task WorkboardWorkerTask) (WorkboardCandidate, error) {
	if ctx == nil || r == nil || r.supervisor == nil || task.BoardID == "" || task.CardID == "" || task.TaskID == "" ||
		task.SessionID == "" || task.Scope == "" || task.ExpectedCardRevision < 1 ||
		task.SubmissionID != "" || task.Execute == nil || task.Validate == nil ||
		(task.WorkerID != "" && !validRuntimeHostWorkerID(task.WorkerID)) ||
		(task.FailureEffect != runtime.NoEffect && task.FailureEffect != runtime.ConfirmedEffect && task.FailureEffect != runtime.UncertainEffect) {
		return WorkboardCandidate{}, ErrAdmission
	}
	if task.Reservation != nil {
		reservation := *task.Reservation
		task.Reservation = &reservation
	}
	card, err := r.repository.GetCard(ctx, task.BoardID, task.CardID)
	if err != nil || card.Revision != task.ExpectedCardRevision || card.State != workboard.Ready ||
		(card.AssigneeID != "" && task.WorkerID != card.AssigneeID) {
		return WorkboardCandidate{}, errors.Join(ErrAdmission, err)
	}
	workerID := task.WorkerID
	if workerID == "" {
		workerID = rand.Text()
	}
	dispatch, err := NewWorkboardWorkerDispatch(r.repository, workerID, r.policy, r.ttl, r.evaluator, r.now)
	if err != nil {
		return WorkboardCandidate{}, err
	}
	var candidate WorkboardCandidate
	var executionErr error
	supervised, stopSupervisor := context.WithCancel(ctx)
	defer stopSupervisor()
	err = r.supervisor.WithSlot(supervised, func(run context.Context) error {
		candidate, executionErr = r.execute(run, stopSupervisor, dispatch, workerID, task)
		return executionErr
	})
	if err != nil {
		return WorkboardCandidate{}, errors.Join(err, executionErr)
	}
	candidate.ArtifactRefs = append([]string{}, candidate.ArtifactRefs...)
	return candidate, nil
}

func (r *WorkboardWorkerRunner) execute(ctx context.Context, stopSupervisor context.CancelFunc, dispatch *WorkboardWorkerDispatch, workerID string,
	task WorkboardWorkerTask,
) (candidate WorkboardCandidate, runErr error) {
	if r.runtimeStore == nil {
		return WorkboardCandidate{}, workers.ErrDurability
	}
	handle := &WorkboardWorkerHandle{dispatch: dispatch, controls: r.repository, repository: r.repository,
		boardID: task.BoardID, cardID: task.CardID, cardRevision: task.ExpectedCardRevision,
		workerID: workerID, taskID: task.TaskID, sessionID: task.SessionID, parentTaskID: task.ParentTaskID,
		claimKey: workboardOperationKey("claim"), runtimeStore: r.runtimeStore,
		controlChanged: make(chan struct{}, 1)}
	defer handle.revoke()
	run, cancel := context.WithCancel(ctx)
	if task.Reservation != nil && task.Reservation.TimeLimitMS > 0 {
		baseCancel := cancel
		var deadlineCancel context.CancelFunc
		run, deadlineCancel = context.WithTimeout(run, time.Duration(task.Reservation.TimeLimitMS)*time.Millisecond)
		cancel = func() {
			deadlineCancel()
			baseCancel()
		}
	}
	if task.Reservation != nil {
		reservation := *task.Reservation
		handle.reservation = &reservation
	}
	handle.bindCancellation(cancel, stopSupervisor)
	heartbeats := make(chan error, 1)
	joined := false
	joinHeartbeat := func() error {
		if joined {
			return nil
		}
		cancel()
		joined = true
		if !handle.heartbeatIsStarted() {
			return nil
		}
		return <-heartbeats
	}
	handle.commitFirst = func(commitCtx context.Context, event runtime.Event) error {
		return handle.claimTaskStart(commitCtx, event, run, r.heartbeat, heartbeats)
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
		if heartbeatErr == nil && handle.claimIsCommitted() {
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
	repository                                    workboard.LifecycleSnapshotRepository
	runtimeStore                                  *telemetry.Store
	boardID, cardID, attemptID, claimID           string
	workerID, taskID, sessionID, parentTaskID     string
	claimKey                                      string
	cardRevision, claimRevision, criteriaRevision int64
	bound, claimCommitted, heartbeatStarted       bool
	active                                        bool
	blocked                                       bool
	paused                                        bool
	controlChanged                                chan struct{}
	cancelCallback, stopSupervisor                context.CancelFunc
	commitFirst                                   func(context.Context, runtime.Event) error
	reservation                                   *workboard.ExecutionReservation
}

// BindRuntimeRequest attaches this pending worker capability to exactly one
// runtime request. It does not claim the card. The claim is committed only when
// the host presents the actual redacted TaskStarted event through commitFirst.
func (h *WorkboardWorkerHandle) BindRuntimeRequest(request Request) (Request, error) {
	if h == nil {
		return Request{}, ErrAdmission
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bound || h.claimCommitted || h.active || h.commitFirst == nil || h.runtimeStore == nil {
		return Request{}, ErrAdmission
	}
	h.bound = true
	return withRuntimeHostAdmission(request, runtimeHostAdmission{taskID: h.taskID, sessionID: h.sessionID,
		parentTaskID: h.parentTaskID, workerID: h.workerID, store: h.runtimeStore, commitFirst: h.commitFirst})
}

func (h *WorkboardWorkerHandle) claimTaskStart(ctx context.Context, event runtime.Event, run context.Context,
	interval time.Duration, heartbeats chan<- error,
) (err error) {
	defer func() {
		if recover() != nil {
			h.revoke()
			err = workers.ErrDurability
		}
	}()
	if ctx == nil || event.Validate() != nil || event.Kind != runtime.TaskStarted || event.Sequence != 1 ||
		event.TaskID != h.taskID || event.SessionID != h.sessionID || event.CorrelationID != h.taskID ||
		event.WorkerID != h.workerID || event.Data.ParentTaskID != h.parentTaskID || event.Data.SubmissionID != "" {
		return ErrAdmission
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.bound || h.claimCommitted || h.active || h.heartbeatStarted || run.Err() != nil {
		return ErrAdmission
	}
	request := workboard.ClaimRequest{BoardID: h.boardID, CardID: h.cardID, IdempotencyKey: h.claimKey,
		ExpectedCardRevision: h.cardRevision, TaskID: h.taskID, SessionID: h.sessionID}
	var receipt workboard.OperationReceipt
	if h.reservation == nil {
		receipt, err = h.dispatch.ClaimTaskStart(ctx, event, request)
	} else {
		receipt, err = h.dispatch.ClaimBudgetedTaskStart(ctx, event, request, *h.reservation)
	}
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		return errors.Join(workers.ErrWork, err)
	}
	h.claimCommitted = true
	snapshots, err := h.repository.ReadCardLifecycleSnapshots(ctx, h.boardID, []string{h.cardID})
	snapshot, found := snapshots[h.cardID]
	card, cardErr := h.runtimeStore.GetCard(ctx, h.boardID, h.cardID)
	if err != nil || cardErr != nil || !found || snapshot.Attempt == nil || snapshot.Attempt.Claim == nil {
		return errors.Join(workers.ErrDurability, err, cardErr)
	}
	attempt, durableClaim := snapshot.Attempt, snapshot.Attempt.Claim
	if snapshot.CardID != h.cardID || attempt.Validate() != nil || attempt.WorkerID != h.workerID ||
		durableClaim.OwnerID != h.workerID || durableClaim.TaskID != h.taskID ||
		!containsExact(attempt.TaskIDs, h.taskID) || !containsExact(attempt.SessionIDs, h.sessionID) ||
		card.Revision != *receipt.CardRevision || card.CurrentAttemptID != attempt.ID || card.CurrentClaimID != durableClaim.ID ||
		durableClaim.Revision != *receipt.ClaimRevision {
		return workers.ErrDurability
	}
	h.attemptID, h.claimID = attempt.ID, durableClaim.ID
	h.cardRevision, h.claimRevision, h.criteriaRevision = *receipt.CardRevision, *receipt.ClaimRevision, attempt.CriteriaRevision
	h.active = true
	h.heartbeatStarted = true
	started := make(chan struct{})
	go h.heartbeat(run, interval, heartbeats, started)
	<-started
	return nil
}

func (h *WorkboardWorkerHandle) claimIsCommitted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claimCommitted
}

func (h *WorkboardWorkerHandle) heartbeatIsStarted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.heartbeatStarted
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

func (h *WorkboardWorkerHandle) heartbeat(ctx context.Context, interval time.Duration, done chan<- error, started chan<- struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	close(started)
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
