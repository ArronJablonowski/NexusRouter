package workers_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func setup(t *testing.T) (*telemetry.Store, *workers.Supervisor) {
	t.Helper()
	s, err := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	sup, err := workers.New(1, 10*time.Millisecond, time.Second, s, s)
	if err != nil {
		t.Fatal(err)
	}
	return s, sup
}
func work(task string) workers.Work {
	return workers.Work{TaskID: task, SessionID: "session", ParentID: "parent", Scope: "project", Execute: func(context.Context) (string, error) { return "answer", nil }, Validate: func(context.Context, string) error { return nil }}
}

func TestWorkerAcceptanceDurable(t *testing.T) {
	s, sup := setup(t)
	out, err := sup.Run(context.Background(), work("child"))
	if err != nil || out != "answer" {
		t.Fatal(out, err)
	}
	events, err := s.Read(context.Background(), "child", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []runtime.Kind{runtime.TaskStarted, runtime.WorkerStarted, runtime.EvaluationRecorded, runtime.WorkerCompleted, runtime.TaskCompleted}
	filtered := []runtime.Kind{}
	for _, e := range events {
		if e.CorrelationID != "child" || (e.Kind == runtime.TaskStarted && e.Data.ParentTaskID != "parent") {
			t.Fatal("task correlation or parent linkage lost")
		}
		if e.Kind != runtime.WorkerHeartbeat {
			filtered = append(filtered, e.Kind)
		}
	}
	if len(filtered) != len(want) {
		t.Fatal(filtered)
	}
	for i := range want {
		if filtered[i] != want[i] {
			t.Fatal(filtered)
		}
	}
	leases, err := s.InspectLeases(context.Background(), "project")
	if err != nil || len(leases) != 0 {
		t.Fatal("lease retained after stop", leases, err)
	}
}

func TestWorkerAuditRunsOnlyAfterValidationAndPersistsBeforeAcceptance(t *testing.T) {
	s, sup := setup(t)
	w := work("audited-child")
	intent := &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	w.DelegationAuditIntent = intent
	var validated atomic.Bool
	w.Validate = func(context.Context, string) error { validated.Store(true); return nil }
	confidence := .8
	w.Review = func(_ context.Context, output string) (*runtime.DelegationAudit, error) {
		if !validated.Load() || output != "answer" {
			t.Fatal("review preceded deterministic validation")
		}
		return &runtime.DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", AuditID: "audit", Status: "completed", Verdict: "accept", Confidence: &confidence, Citations: []string{"candidate"}}, nil
	}
	if output, err := sup.Run(context.Background(), w); err != nil || output != "answer" {
		t.Fatal(output, err)
	}
	events, err := s.Read(context.Background(), "audited-child", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Data.DelegationAuditIntent == nil {
		t.Fatal("audit intent was not durable before execution")
	}
	var completed runtime.Event
	for _, event := range events {
		if event.Kind == runtime.WorkerCompleted {
			completed = event
		}
	}
	if completed.Data.DelegationAudit == nil || completed.Data.DelegationAudit.Status != "completed" {
		t.Fatal("audit outcome not bound to accepted output", completed)
	}
}

func TestWorkerAuditSkipsInvalidOutputAndCancellation(t *testing.T) {
	for _, mode := range []string{"failed", "invalid", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			_, sup := setup(t)
			w := work("audit-" + mode)
			w.DelegationAuditIntent = &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
			var reviews atomic.Int32
			w.Review = func(context.Context, string) (*runtime.DelegationAudit, error) {
				reviews.Add(1)
				return nil, errors.New("must not run")
			}
			ctx := context.Background()
			if mode == "failed" {
				w.Execute = func(context.Context) (string, error) { return "private partial", errors.New("failed") }
			} else if mode == "invalid" {
				w.Validate = func(context.Context, string) error { return errors.New("invalid") }
			} else {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if output, err := sup.Run(ctx, w); err == nil || output != "" || reviews.Load() != 0 {
				t.Fatal(output, err, reviews.Load())
			}
		})
	}
}

func TestWorkerAdvisoryDispositionNeverOverridesValidation(t *testing.T) {
	for _, status := range []string{"failed", "not_run", "abstained"} {
		t.Run(status, func(t *testing.T) {
			s, sup := setup(t)
			w := work("advisory-" + status)
			intent := &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
			w.DelegationAuditIntent = intent
			w.Review = func(context.Context, string) (*runtime.DelegationAudit, error) {
				result := &runtime.DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", Status: status, Citations: []string{}}
				if status == "abstained" {
					confidence := 0.0
					result.AuditID, result.Verdict, result.Confidence = "audit", "abstain", &confidence
				}
				return result, nil
			}
			if output, err := sup.Run(context.Background(), w); err != nil || output != "answer" {
				t.Fatal("advisory disposition changed deterministic success", output, err)
			}
			events, err := s.Read(context.Background(), "advisory-"+status, 0, 100)
			if err != nil || events[len(events)-1].Kind != runtime.TaskCompleted {
				t.Fatal(events, err)
			}
		})
	}
}

func TestWorkerCancellationDuringActiveAuditCannotPublishOutput(t *testing.T) {
	s, sup := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := work("audit-canceled-active")
	w.DelegationAuditIntent = &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	started, release := make(chan struct{}), make(chan struct{})
	w.Review = func(context.Context, string) (*runtime.DelegationAudit, error) {
		close(started)
		<-release // Model an adapter that joins late despite cancellation.
		return &runtime.DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", Status: "failed", Citations: []string{}}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := sup.Run(ctx, w); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatal("worker returned before reviewer joined", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	events, err := s.Read(context.Background(), "audit-canceled-active", 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TaskCanceled {
		t.Fatal(events, err)
	}
	for _, event := range events {
		if event.Kind == runtime.WorkerCompleted || event.Kind == runtime.EvaluationRecorded {
			t.Fatal("canceled audit output was accepted", event)
		}
	}
}

func TestWorkerLeaseLossDuringActiveAuditCannotPublishOutput(t *testing.T) {
	s, _ := setup(t)
	sup, err := workers.New(1, time.Millisecond, time.Second, failingLease{s}, s)
	if err != nil {
		t.Fatal(err)
	}
	w := work("audit-lease-lost")
	w.DelegationAuditIntent = &runtime.DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	started := make(chan struct{})
	w.Review = func(ctx context.Context, _ string) (*runtime.DelegationAudit, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := sup.Run(context.Background(), w); done <- err }()
	<-started
	if err := <-done; err != workers.ErrWork {
		t.Fatal(err)
	}
	events, err := s.Read(context.Background(), "audit-lease-lost", 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TaskFailed {
		t.Fatal(events, err)
	}
	for _, event := range events {
		if event.Kind == runtime.WorkerCompleted || event.Kind == runtime.EvaluationRecorded {
			t.Fatal("lease-lost audit output was accepted", event)
		}
	}
}

func TestWorkerRejectsFailureAndPanic(t *testing.T) {
	for _, stage := range []string{"execute", "validate", "panic"} {
		t.Run(stage, func(t *testing.T) {
			s, sup := setup(t)
			w := work("child")
			switch stage {
			case "execute":
				w.Execute = func(context.Context) (string, error) { return "unsafe", errors.New("private error") }
			case "validate":
				w.Validate = func(context.Context, string) error { return errors.New("rejected") }
			case "panic":
				w.Execute = func(context.Context) (string, error) { panic("private panic") }
			}
			out, err := sup.Run(context.Background(), w)
			if err != workers.ErrWork || out != "" {
				t.Fatal(out, err)
			}
			events, err := s.Read(context.Background(), "child", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if events[len(events)-1].Kind != runtime.TaskFailed {
				t.Fatal(events)
			}
			for _, e := range events {
				if e.Kind == runtime.WorkerCompleted || e.Kind == runtime.EvaluationRecorded {
					t.Fatal("rejected output accepted")
				}
			}
		})
	}
}

func TestBoundHeldUntilCanceledExecutionStops(t *testing.T) {
	_, sup := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	stopped := make(chan struct{})
	finish := make(chan struct{})
	w := work("first")
	w.Execute = func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		<-finish
		return "", ctx.Err()
	}
	done := make(chan error, 1)
	defer func() {
		close(finish)
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("worker did not stop")
		}
	}()
	go func() { _, err := sup.Run(ctx, w); done <- err }()
	<-started
	cancel()
	<-stopped
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer secondCancel()
	second := work("second")
	second.Execute = func(context.Context) (string, error) { t.Error("slot reused before stopped"); return "", nil }
	if _, err := sup.Run(secondCtx, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// finish is closed by defer only after verifying the slot remained held.
}

type failingLease struct{ *telemetry.Store }

func (f failingLease) RenewLease(context.Context, string, string, time.Time, time.Duration) error {
	return errors.New("lease lost")
}
func TestLeaseLossCancelsWorker(t *testing.T) {
	s, _ := setup(t)
	sup, err := workers.New(1, time.Millisecond, time.Second, failingLease{s}, s)
	if err != nil {
		t.Fatal(err)
	}
	w := work("lost")
	w.Execute = func(ctx context.Context) (string, error) { <-ctx.Done(); return "unaccepted", nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := sup.Run(ctx, w); out != "" || err != workers.ErrWork {
		t.Fatal(out, err)
	}
}

func TestRunningWorkerPersistsHeartbeatBeforeCompletion(t *testing.T) {
	s, sup := setup(t)
	started := make(chan struct{})
	release := make(chan struct{})
	w := work("heartbeat")
	w.Execute = func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-release:
			return "answer", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		output, err := sup.Run(ctx, w)
		done <- result{output: output, err: err}
	}()
	<-started

	heartbeat := false
	for !heartbeat {
		events, err := s.Read(ctx, "heartbeat", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == runtime.WorkerHeartbeat {
				heartbeat = true
				break
			}
		}
		if heartbeat {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker heartbeat was not persisted")
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(release)
	got := <-done
	if got.err != nil || got.output != "answer" {
		t.Fatal(got.output, got.err)
	}
	events, err := s.Read(context.Background(), "heartbeat", 0, 100)
	if err != nil || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatal(events, err)
	}
	leases, err := s.InspectLeases(context.Background(), "project")
	if err != nil || len(leases) != 0 {
		t.Fatal("completed worker retained lease", leases, err)
	}
}
