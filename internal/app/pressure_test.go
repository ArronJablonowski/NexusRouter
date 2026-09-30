package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestPressureWaitTimeoutAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelEarly], func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "100ms"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			result, err := s.runWithPressure(ctx, Request{}, func(actual context.Context, r Request) (Result, error) {
				calls++
				if actual != ctx || r.admissionContext == nil {
					t.Fatal("wrong contexts")
				}
				if cancelEarly {
					cancel()
				}
				return Result{}, resources.ErrCapacity
			})
			expected := ErrPressureTimeout
			if cancelEarly {
				expected = context.Canceled
			}
			if !errors.Is(err, ErrAdmission) || !errors.Is(err, resources.ErrCapacity) || !errors.Is(err, expected) || result.TaskID != "" || calls < 1 || calls > 5 || cancelEarly && calls != 1 {
				t.Fatalf("result %+v err %v calls %d", result, err, calls)
			}
		})
	}
}

func TestPressureTimeoutDoesNotWaitForResourceMutexOwner(t *testing.T) {
	for _, model := range []string{"a", "auto"} {
		t.Run(model, func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "100ms"
			s.mu.Lock()
			defer s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				out, err := s.Run(ctx, Request{ModelID: model, Prompt: "hello"})
				if out.TaskID != "" {
					done <- errors.New("dispatched while lock unavailable")
					return
				}
				done <- err
			}()
			// The mutex stays held until AFTER admission returns.
			select {
			case err := <-done:
				if !errors.Is(err, ErrPressureTimeout) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("queue deadline blocked behind another profiler")
			}
		})
	}
}

func TestPressureWaitDoesNotReplayStartedOrOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result Result
		err    error
	}{{"admission", Result{}, ErrAdmission}, {"provider", Result{TaskID: "task"}, errors.New("provider failed")}, {"startedcapacity", Result{TaskID: "task"}, resources.ErrCapacity}, {"success", Result{TaskID: "task", Text: "answer"}, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "100ms"
			calls := 0
			out, err := s.runWithPressure(context.Background(), Request{}, func(context.Context, Request) (Result, error) { calls++; return tc.result, tc.err })
			if calls != 1 || out.TaskID != tc.result.TaskID || err != tc.err {
				t.Fatalf("replayed %d %+v %v", calls, out, err)
			}
		})
	}
}

func TestPressureAdmissionBudgetDoesNotLimitExecution(t *testing.T) {
	s, _ := autoFixture(t)
	s.settings.Hardware.LocalPressurePolicy = "wait"
	s.settings.Hardware.LocalQueueTimeout = "100ms"
	outer, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := s.runWithPressure(outer, Request{}, func(actual context.Context, r Request) (Result, error) {
		<-r.admissionContext.Done()
		if actual.Err() != nil {
			t.Fatal("queue budget canceled execution")
		}
		return Result{TaskID: "admitted", Text: "answer"}, nil
	})
	if err != nil || out.Text != "answer" {
		t.Fatal(out, err)
	}
}

func TestPressureRejectDefaultAndWaitRetries(t *testing.T) {
	s, _ := autoFixture(t)
	calls := 0
	_, err := s.runWithPressure(context.Background(), Request{}, func(context.Context, Request) (Result, error) { calls++; return Result{}, resources.ErrCapacity })
	if err != resources.ErrCapacity || calls != 1 {
		t.Fatal(calls, err)
	}
	s.settings.Hardware.LocalPressurePolicy = "wait"
	s.settings.Hardware.LocalQueueTimeout = "1s"
	calls = 0
	var admittedTaskID, admittedSessionID string
	out, err := s.runWithPressure(context.Background(), Request{}, func(_ context.Context, request Request) (Result, error) {
		calls++
		if admittedTaskID == "" {
			admittedTaskID, admittedSessionID = request.taskID, request.sessionID
		} else if request.taskID != admittedTaskID || request.sessionID != admittedSessionID {
			t.Fatal("pressure retry changed frozen execution identity")
		}
		if request.taskID == "" || request.sessionID != request.taskID {
			t.Fatal("pressure retry did not preallocate a fresh task and session identity")
		}
		if calls == 1 {
			return Result{}, resources.ErrCapacity
		}
		return Result{TaskID: "task"}, nil
	})
	if err != nil || out.TaskID != "task" || calls != 2 {
		t.Fatal(out, err, calls)
	}
}

func TestPressureExplicitCapacityRetryAndProfileFailure(t *testing.T) {
	for _, profileFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovers", true: "profile_failure"}[profileFailure], func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "1s"
			calls := 0
			s.profile = func(context.Context) (resources.Snapshot, error) {
				calls++
				if profileFailure {
					return resources.Snapshot{}, errors.New("private sensor failure")
				}
				available := uint64(1000)
				if calls == 1 {
					available = 0
				}
				return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: available}, nil
			}
			out, err := s.runWithPressure(context.Background(), Request{ModelID: "a", Prompt: "hello"}, s.runExplicit)
			if profileFailure {
				if !errors.Is(err, ErrAdmission) || errors.Is(err, resources.ErrCapacity) || calls != 1 || out.TaskID != "" {
					t.Fatal(out, err, calls)
				}
			} else if err != nil || out.TaskID == "" || calls != 2 {
				t.Fatal(out, err, calls)
			}
		})
	}
}

func TestPressureDoesNotRetryUnknownResourceData(t *testing.T) {
	for _, mode := range []string{"stale", "invalid", "missing_gpu", "unified_gpu"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "1s"
			if mode == "missing_gpu" || mode == "unified_gpu" {
				s.settings.Models[0].VRAMBytes = 1
			}
			calls := 0
			s.profile = func(context.Context) (resources.Snapshot, error) {
				calls++
				snapshot := resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}
				switch mode {
				case "stale":
					snapshot.Time = time.Now().Add(-time.Minute)
				case "invalid":
					snapshot.AvailableRAM = 1001
				case "unified_gpu":
					snapshot.UnifiedMemory = true
				}
				return snapshot, nil
			}
			out, err := s.runWithPressure(context.Background(), Request{ModelID: "a", Prompt: "hello"}, s.runExplicit)
			if !errors.Is(err, ErrAdmission) || !errors.Is(err, resources.ErrResourceData) || errors.Is(err, resources.ErrCapacity) || calls != 1 || out.TaskID != "" {
				t.Fatal(out, err, calls)
			}
		})
	}
}
