package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

// Dispatcher requeues only expired claims with no durable task start, or
// projects already-terminal histories without reexecution. Partial work is
// never replayed automatically.
type Dispatcher struct {
	cancel                  context.CancelFunc
	done                    chan struct{}
	db                      *telemetry.Store
	eventSink               runtime.EventSink
	eventSinkSequencer      *configuredSinkSequencer
	lifecycle               context.Context
	recoverySecrets         func() []string
	once                    sync.Once
	mu                      sync.Mutex
	err                     error
	configuredWorkers       int
	startedWorkers          int
	workerAlive             map[int]bool
	workerBeats             map[int]time.Time
	reconcilerStarted       bool
	reconcilerAlive         bool
	reconcilerBeat          time.Time
	closing                 bool
	closed                  bool
	healthNow               func() time.Time
	renewInterval           time.Duration
	workboardRecovery       *WorkboardRecoveryCoordinator
	compactionRecoveryAfter string
	summaryRecoveryAfter    string
}

func StartDispatcher(ctx context.Context, s *Service) (*Dispatcher, error) {
	if s == nil || s.settings.Workers.Max < 1 || s.settings.Workers.Max > 64 {
		return nil, ErrAdmission
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return nil, ErrSubmission
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &Dispatcher{cancel: cancel, done: make(chan struct{}), db: db, eventSink: s.eventSink, eventSinkSequencer: s.eventSinkSequencer, lifecycle: ctx, recoverySecrets: func() []string { return memorySecrets(s.settings, s.secret) }, configuredWorkers: s.settings.Workers.Max, workerAlive: map[int]bool{}, workerBeats: map[int]time.Time{}}
	d.workboardRecovery, err = NewWorkboardRecoveryCoordinator(db, time.Now)
	if err != nil {
		db.Close()
		cancel()
		return nil, ErrSubmission
	}
	// Reconcile one bounded page before workers or the HTTP daemon can dispatch
	// new work. Remaining pages are drained by the periodic reconciler.
	// Recover context-compaction owners first because that transaction also
	// closes its in-flight summary attempt. Running the generic summary sweep
	// first would strand the enclosing plan lifecycle in `started`.
	d.compactionRecoveryAfter, _, err = db.ReconcileContextCompactionPlansPage(ctx, "", 32, time.Now().UTC())
	if err != nil {
		db.Close()
		cancel()
		return nil, ErrSubmission
	}
	d.summaryRecoveryAfter, _, err = db.ReconcileSummaryAttemptsPage(ctx, "", 32, time.Now().UTC())
	if err != nil {
		db.Close()
		cancel()
		return nil, ErrSubmission
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		d.supervisorStarted(-1)
		defer d.supervisorStopped(-1)
		d.reconcile(ctx, s.submissionConfigDigest())
	}()
	for i := 0; i < s.settings.Workers.Max; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			d.supervisorStarted(i)
			defer d.supervisorStopped(i)
			d.worker(ctx, s, i)
		}()
	}
	go func() { workers.Wait(); close(d.done) }()
	return d, nil
}

func (d *Dispatcher) Close() error {
	d.once.Do(func() {
		d.mu.Lock()
		d.closing = true
		d.mu.Unlock()
		d.cancel()
		<-d.done
		if d.db.Close() != nil {
			d.recordError()
		}
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *Dispatcher) recordError() { d.mu.Lock(); d.err = ErrSubmission; d.mu.Unlock() }

func transientClaimError(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func (d *Dispatcher) worker(ctx context.Context, s *Service, id int) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		d.supervisorHeartbeat(id)
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		claim, err := d.db.ClaimSubmission(query, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
		cancel()
		if err == nil {
			if d.executeWorkerContained(ctx, s, claim, id) {
				// The claim may have crossed an unknown side-effect boundary. Keep
				// it fenced until ordinary lease-expiry reconciliation can inspect
				// durable history, while retaining this worker for unrelated work.
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			continue
		}
		if !transientClaimError(err) && ctx.Err() == nil {
			// A bounded claim query can fail transiently under SQLite contention or
			// deadline pressure while another request owns the single store connection.
			// Deadline and cancellation are expected backpressure; other failures remain
			// visible as degraded health. The next iteration always opens a fresh query.
			d.recordError()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// executeWorkerContained is the worker-loop's last-resort containment boundary.
// It deliberately records neither the recovered value nor a synthetic terminal
// event: durable history and lease-expiry recovery remain the authority for an
// execution whose side-effect state is unknown.
func (d *Dispatcher) executeWorkerContained(ctx context.Context, s *Service, claim submissions.Claim, workerID int) (panicked bool) {
	defer func() {
		if recover() != nil {
			d.recordError()
			panicked = true
		}
	}()
	d.executeWorker(ctx, s, claim, workerID)
	return false
}

func (d *Dispatcher) execute(ctx context.Context, s *Service, claim submissions.Claim) {
	d.executeWorker(ctx, s, claim, -2)
}

func (d *Dispatcher) executeWorker(ctx context.Context, s *Service, claim submissions.Claim, workerID int) {
	job, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	heartbeat, stopHeartbeat := context.WithCancel(job)
	heartbeatDone := make(chan error, 1)
	go func() {
		d.mu.Lock()
		interval := d.renewInterval
		d.mu.Unlock()
		if interval <= 0 {
			interval = 5 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeat.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
			}
			d.supervisorHeartbeat(workerID)
			query, stop := context.WithTimeout(heartbeat, 5*time.Second)
			cancelRequested, err := d.db.RenewSubmission(query, claim.Status.ID, claim.Token, time.Now().UTC(), 30*time.Second)
			stop()
			d.supervisorHeartbeat(workerID)
			if heartbeat.Err() != nil {
				heartbeatDone <- nil
				return
			}
			if err != nil || cancelRequested {
				cancel()
				heartbeatDone <- err
				return
			}
		}
	}()
	joinHeartbeat := func() error {
		if heartbeatDone == nil {
			return nil
		}
		stopHeartbeat()
		err := <-heartbeatDone
		heartbeatDone = nil
		return err
	}
	defer joinHeartbeat()
	envelope, err := decodeSubmissionEnvelope(claim.Request)
	r := envelope.Request
	if err == nil && envelope.Branch != nil {
		err = d.db.ValidateBranchSubmission(job, claim.Status.ID)
		if errors.Is(err, submissions.ErrInvalid) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessions.ErrHistory) {
			err = ErrAdmission
		}
	}
	if err == nil && envelope.Resume != nil {
		err = d.db.ValidateResumeSubmission(job, claim.Status.ID)
		if errors.Is(err, submissions.ErrInvalid) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessions.ErrHistory) {
			err = ErrAdmission
		}
	}
	var out Result
	if err == nil {
		err = d.awaitContinuation(job, r.ContinueTaskID)
	}
	if err == nil {
		r.submissionID, r.submissionToken = claim.Status.ID, claim.Token
		out, err = s.Run(job, r)
	}
	leaseErr := joinHeartbeat()
	state, code := "succeeded", ""
	if err != nil {
		state, code = "failed", "execution_failed"
	}
	if errors.Is(err, ErrAdmission) {
		state, code = "failed", "admission_denied"
	}
	if job.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		state, code = "canceled", "canceled"
	}
	if leaseErr != nil || errors.Is(err, runtime.ErrExecutionLeaseLost) {
		state, code = "failed", "interrupted"
	}
	result := &submissions.Result{TaskID: out.TaskID, Text: out.Text, Turns: out.Turns, FinishReason: out.FinishReason, Usage: out.Usage, PreviousTaskIDs: out.PreviousTaskIDs, RouteEstimatedCost: out.RouteEstimatedCost, AuditID: out.AuditID, AuditStatus: out.AuditStatus}
	if state != "succeeded" {
		result.Text = ""
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	status, statusErr := d.db.Submission(finish, claim.Status.ID)
	if statusErr != nil {
		d.recordError()
		return
	}
	linked := make(map[string]bool, len(status.TaskIDs))
	for _, id := range status.TaskIDs {
		linked[id] = true
	}
	if !linked[result.TaskID] {
		result = nil
	} else {
		previous := make([]string, 0, len(result.PreviousTaskIDs))
		for _, id := range result.PreviousTaskIDs {
			if linked[id] {
				previous = append(previous, id)
			}
		}
		result.PreviousTaskIDs = previous
	}
	if status.CancelRequested {
		state, code = "canceled", "canceled"
		if result != nil {
			result.Text = ""
		}
	}
	_, finishErr := d.db.FinishSubmission(finish, claim.Status.ID, claim.Token, state, code, result)
	if state == "succeeded" && (errors.Is(finishErr, submissions.ErrConflict) || errors.Is(finishErr, submissions.ErrLeaseLost)) {
		// A cancellation or lease expiry can win after the status read. Never
		// retry execution; only finalize the already-returned worker safely.
		current, readErr := d.db.Submission(finish, claim.Status.ID)
		if readErr == nil && current.State == "running" {
			if current.CancelRequested {
				state, code = "canceled", "canceled"
			} else {
				state, code = "failed", "interrupted"
			}
			if result != nil {
				result.Text = ""
			}
			_, finishErr = d.db.FinishSubmission(finish, claim.Status.ID, claim.Token, state, code, result)
		}
	}
	// Another supervisor may have fenced this undispatched owner. Its terminal
	// write is expected to be denied; it must not poison unrelated active work.
	if finishErr != nil && !errors.Is(finishErr, submissions.ErrLeaseLost) {
		d.recordError()
	}
}

// awaitContinuation holds the already-bounded submission worker and its
// renewable claim while an explicitly referenced source is still running.
// It does not repair, retry or infer from the source. Once the source becomes
// terminal, normal continuation admission decides whether its history is safe.
func (d *Dispatcher) awaitContinuation(ctx context.Context, task string) error {
	if task == "" {
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := d.db.TaskContinuation(ctx, task)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessions.ErrHistory) {
				return ErrAdmission
			}
			return err
		}
		if status.State != "running" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
