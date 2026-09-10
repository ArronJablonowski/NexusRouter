// Package workers implements bounded in-process read-only delegation.
package workers

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type Lease struct {
	Token, TaskID, Owner, Scope string
	Writer                      bool
	Expires                     time.Time
	Released                    bool
}
type LeaseStore interface {
	AcquireLease(context.Context, string, string, string, bool, time.Time, time.Duration) (Lease, error)
	RenewLease(context.Context, string, string, time.Time, time.Duration) error
	// Release only after all holder work has joined; neither reader nor writer
	// expiry proves that an in-process callback stopped using the resource.
	ReleaseLease(context.Context, string, string) error
}

// LeaseJournal atomically checks worker ownership with each durable append.
// Production hosts should implement this on the same store as LeaseStore.
type LeaseJournal interface {
	AppendLeased(context.Context, int64, runtime.Event, string, string) error
}

// FinalizingLeaseJournal atomically appends a worker terminal event and releases
// its exact lease. Calling it asserts that Execute and Validate have joined.
// An acknowledgement is required before a Supervisor releases accepted output.
type FinalizingLeaseJournal interface {
	FinishLeased(context.Context, int64, runtime.Event, string, string) error
}
type Work struct {
	TaskID, SessionID, ParentID, Scope string
	// WorkerID is an optional trusted host-supplied identity. It exists so an
	// application capability can bind this execution to another durable lease
	// before Execute begins. Prompts and tool arguments must never populate it.
	// An empty value preserves the supervisor's generated-identity behavior.
	WorkerID              string
	SubmissionID          string
	DelegationOrigin      *runtime.DelegationOrigin
	DelegationAuditIntent *runtime.DelegationAuditIntent
	// Execute must honor cancellation. Only inference and read-only tools are
	// admitted here; there is no safe forced termination of arbitrary Go code.
	Execute  func(context.Context) (string, error)
	Validate func(context.Context, string) error
	// Review runs only after Execute and deterministic Validate succeed. It is
	// advisory: expected reviewer failure is represented by a valid durable
	// projection, while an error means the projection cannot safely be recorded.
	Review func(context.Context, string) (*runtime.DelegationAudit, error)
}
type Supervisor struct {
	slots          chan struct{}
	store          LeaseStore
	journal        runtime.Journal
	heartbeat, ttl time.Duration
}

var ErrWork = errors.New("worker failed or output rejected")
var ErrDurability = errors.New("worker durable state unavailable")

func New(limit int, heartbeat, ttl time.Duration, store LeaseStore, journal runtime.Journal) (*Supervisor, error) {
	if limit < 1 || limit > 64 || heartbeat < time.Millisecond || ttl <= 2*heartbeat || ttl > 10*time.Minute || store == nil || journal == nil {
		return nil, ErrWork
	}
	return &Supervisor{make(chan struct{}, limit), store, journal, heartbeat, ttl}, nil
}

// Run waits for a slot and does not release it while a canceled callback is
// still running. Parent-child scope and deny-rule derivation belong to admission.
// Each child owns a separate task log, correlated to the durable parent task.
func (s *Supervisor) Run(ctx context.Context, w Work) (output string, runErr error) {
	if w.TaskID == "" || w.SessionID == "" || w.ParentID == "" || w.Scope == "" || w.Execute == nil || w.Validate == nil {
		return "", ErrWork
	}
	if w.WorkerID != "" && !validWorkerID(w.WorkerID) {
		return "", ErrWork
	}
	if w.DelegationOrigin != nil {
		w.DelegationOrigin = w.DelegationOrigin.Clone()
		if w.DelegationOrigin.Validate() != nil {
			return "", ErrWork
		}
	}
	if (w.DelegationAuditIntent == nil) != (w.Review == nil) {
		return "", ErrWork
	}
	if w.DelegationAuditIntent != nil {
		intent := *w.DelegationAuditIntent
		if intent.Validate() != nil {
			return "", ErrWork
		}
		w.DelegationAuditIntent = &intent
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.slots }()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	worker := w.WorkerID
	if worker == "" {
		worker = rand.Text()
	}
	leaseToken := ""
	leaseFinalized := false
	seq := int64(0)
	persist := func(ctx context.Context, kind runtime.Kind, data runtime.Data) error {
		e := runtime.Event{Version: 1, ID: rand.Text(), TaskID: w.TaskID, SessionID: w.SessionID, CorrelationID: w.TaskID, WorkerID: worker, Sequence: seq + 1, Time: time.Now().UTC(), Kind: kind, Data: data}
		var err error
		finalizer, canFinalize := s.journal.(FinalizingLeaseJournal)
		terminal := kind == runtime.TaskCompleted || kind == runtime.TaskFailed || kind == runtime.TaskCanceled
		if canFinalize && leaseToken != "" && terminal {
			err = finalizeWorkerLease(finalizer, ctx, seq, e, leaseToken, worker)
			if err == nil {
				leaseFinalized = true
			}
		} else if fenced, ok := s.journal.(LeaseJournal); ok && leaseToken != "" {
			err = fenced.AppendLeased(ctx, seq, e, leaseToken, worker)
		} else {
			err = s.journal.Append(ctx, seq, e)
		}
		if err != nil {
			return ErrDurability
		}
		seq++
		return nil
	}
	finish := func(cause error) error {
		terminal, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		kind := runtime.TaskFailed
		if ctx.Err() != nil {
			kind = runtime.TaskCanceled
			cause = ctx.Err()
		}
		if persist(terminal, kind, runtime.Data{Code: "worker_failed"}) != nil {
			return errors.Join(cause, ErrDurability)
		}
		return cause
	}
	if err := persist(ctx, runtime.TaskStarted, runtime.Data{ParentTaskID: w.ParentID, SubmissionID: w.SubmissionID, DelegationOrigin: w.DelegationOrigin, DelegationAuditIntent: w.DelegationAuditIntent}); err != nil {
		return "", err
	}
	l, err := s.store.AcquireLease(ctx, w.TaskID, worker, w.Scope, false, time.Now(), s.ttl)
	if err != nil {
		return "", finish(ErrWork)
	}
	leaseToken = l.Token
	// Defer runs only after Execute and Validate have returned.
	defer func() {
		if leaseFinalized {
			return
		}
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		defer func() {
			if recover() != nil {
				output, runErr = "", errors.Join(runErr, ErrDurability, ctx.Err())
			}
		}()
		if s.store.ReleaseLease(release, l.Token, worker) != nil {
			output, runErr = "", errors.Join(runErr, ErrDurability, ctx.Err())
		}
	}()
	if err := persist(ctx, runtime.WorkerStarted, runtime.Data{}); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", finish(ctx.Err())
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		text  string
		audit *runtime.DelegationAudit
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		result := outcome{}
		defer func() {
			if recover() != nil {
				result = outcome{err: ErrWork}
			}
			done <- result
		}()
		result.text, result.err = w.Execute(run)
		if result.err == nil && run.Err() == nil && len(result.text) <= 1<<20 {
			result.err = w.Validate(run, result.text)
		} else {
			result.err = ErrWork
		}
		if result.err == nil && w.Review != nil {
			result.audit, result.err = w.Review(run, result.text)
			if result.err == nil && (result.audit == nil || result.audit.Validate(w.DelegationAuditIntent) != nil) {
				result.err = ErrWork
			}
		}
	}()
	joined := false
	defer func() {
		if recover() != nil {
			// Even a trusted lease/journal adapter panic cannot abandon active
			// callback work and let the earlier lease-release defer run early.
			cancel()
			if !joined {
				<-done
			}
			output, runErr = "", errors.Join(ErrDurability, ctx.Err())
		}
	}()
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	var failure error
	cancelSignal := ctx.Done()
	for {
		select {
		case <-cancelSignal:
			failure = ctx.Err()
			cancel()
			cancelSignal = nil
		case <-ticker.C:
			if failure != nil {
				continue
			}
			if s.store.RenewLease(run, l.Token, worker, time.Now(), s.ttl) != nil {
				failure = ErrWork
				cancel()
				continue
			}
			if persist(run, runtime.WorkerHeartbeat, runtime.Data{}) != nil {
				failure = ErrDurability
				cancel()
			}
		case result := <-done:
			joined = true
			if failure != nil {
				if failure == ErrDurability {
					return "", failure
				}
				return "", finish(failure)
			}
			if ctx.Err() != nil {
				return "", finish(ctx.Err())
			}
			if result.err != nil {
				return "", finish(ErrWork)
			}
			// Verify ownership after execution/validation before accepting output.
			if s.store.RenewLease(ctx, l.Token, worker, time.Now(), s.ttl) != nil {
				return "", finish(ErrWork)
			}
			accepted := true
			if err := persist(ctx, runtime.EvaluationRecorded, runtime.Data{Accepted: &accepted, Code: "worker_validator"}); err != nil {
				return "", err
			}
			if ctx.Err() != nil {
				return "", finish(ctx.Err())
			}
			if err := persist(ctx, runtime.WorkerCompleted, runtime.Data{Text: result.text, DelegationAudit: result.audit}); err != nil {
				return "", err
			}
			if ctx.Err() != nil {
				return "", finish(ctx.Err())
			}
			if err := persist(ctx, runtime.TaskCompleted, runtime.Data{}); err != nil {
				return "", err
			}
			return result.text, nil
		}
	}
}

func validWorkerID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func finalizeWorkerLease(j FinalizingLeaseJournal, ctx context.Context, seq int64, e runtime.Event, token, owner string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrDurability
		}
	}()
	return j.FinishLeased(ctx, seq, e, token, owner)
}
