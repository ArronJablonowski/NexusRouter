package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/submissions"
)

// Dispatcher requeues only expired claims with no durable task start, or
// projects already-terminal histories without reexecution. Partial work is
// never replayed automatically.
type Dispatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
	db     *telemetry.Store
	once   sync.Once
	mu     sync.Mutex
	err    error
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
	d := &Dispatcher{cancel: cancel, done: make(chan struct{}), db: db}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); d.reconcile(ctx, s.submissionConfigDigest()) }()
	for i := 0; i < s.settings.Workers.Max; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); d.worker(ctx, s) }()
	}
	go func() { workers.Wait(); close(d.done) }()
	return d, nil
}

func (d *Dispatcher) Close() error {
	d.once.Do(func() {
		d.cancel()
		<-d.done
		if d.db.Close() != nil {
			d.recordError()
		}
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *Dispatcher) recordError() { d.mu.Lock(); d.err = ErrSubmission; d.mu.Unlock() }

func (d *Dispatcher) worker(ctx context.Context, s *Service) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		claim, err := d.db.ClaimSubmission(query, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
		cancel()
		if err == nil {
			d.execute(ctx, s, claim)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			d.recordError()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) execute(ctx context.Context, s *Service, claim submissions.Claim) {
	job, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	heartbeat, stopHeartbeat := context.WithCancel(job)
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeat.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
			}
			query, stop := context.WithTimeout(heartbeat, 5*time.Second)
			cancelRequested, err := d.db.RenewSubmission(query, claim.Status.ID, claim.Token, time.Now().UTC(), 30*time.Second)
			stop()
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
	r, err := decodeSubmission(claim.Request)
	var out Result
	if err == nil {
		r.submissionID, r.submissionToken = claim.Status.ID, claim.Token
		out, err = s.Run(job, r)
	}
	stopHeartbeat()
	leaseErr := <-heartbeatDone
	state, code := "succeeded", ""
	if err != nil {
		state, code = "failed", "execution_failed"
	}
	if job.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		state, code = "canceled", "canceled"
	}
	if leaseErr != nil || errors.Is(err, runtime.ErrExecutionLeaseLost) {
		state, code = "failed", "interrupted"
	}
	result := &submissions.Result{TaskID: out.TaskID, Text: out.Text, Turns: out.Turns, FinishReason: out.FinishReason, Usage: out.Usage, PreviousTaskIDs: out.PreviousTaskIDs, AuditID: out.AuditID, AuditStatus: out.AuditStatus}
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
