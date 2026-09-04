package workers_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/runtime"
	"darwinrouter/workers"
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
		if e.CorrelationID != "parent" {
			t.Fatal("parent correlation lost")
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
