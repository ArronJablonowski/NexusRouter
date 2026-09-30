package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestAppendWorkerLeaseFence(t *testing.T) {
	for _, mode := range []string{"valid", "missing-token", "missing-owner", "wrong-token", "wrong-owner", "expired", "released", "reassigned", "wrong-task"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := leaseStore(t)
			ctx := context.Background()
			lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			token, owner := lease.Token, lease.Owner
			switch mode {
			case "missing-token":
				token = ""
			case "missing-owner":
				owner = ""
			case "wrong-token":
				token = "wrong"
			case "wrong-owner":
				owner = "wrong"
			case "expired":
				_, err = s.db.Exec("UPDATE resource_leases SET expires=1")
			case "released":
				err = s.ReleaseLease(ctx, token, owner)
			case "reassigned":
				_, err = s.db.Exec("UPDATE resource_leases SET owner='replacement'")
			case "wrong-task":
				other := event("other-start", 1, runtime.TaskStarted)
				other.TaskID = "other"
				if err = s.Append(ctx, 0, other); err != nil {
					t.Fatal(err)
				}
				_, err = s.db.Exec("UPDATE resource_leases SET task_id='other'")
			}
			if err != nil {
				t.Fatal(err)
			}
			e := event("worker", 2, runtime.WorkerStarted)
			e.WorkerID = "worker"
			err = s.AppendWorker(ctx, 1, e, token, owner, "", "")
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if err = s.ReleaseLease(ctx, token, owner); err != nil {
					t.Fatal(err)
				}
				if err = s.AppendWorker(ctx, 1, e, token, owner, "", ""); err != nil {
					t.Fatalf("exact retry: %v", err)
				}
			} else if !errors.Is(err, runtime.ErrExecutionLeaseLost) {
				t.Fatalf("got %v", err)
			}
			events, err := s.Read(ctx, "task", 0, 100)
			want := 1
			if mode == "valid" {
				want = 2
			}
			if err != nil || len(events) != want {
				t.Fatalf("events=%d err=%v", len(events), err)
			}
		})
	}
}

func TestAppendWorkerExpiredCancellationCleanup(t *testing.T) {
	s, _ := leaseStore(t)
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now().Add(-time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e := event("cancel", 2, runtime.TaskCanceled)
	if err = s.AppendWorker(ctx, 1, e, lease.Token, "wrong", "", ""); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal(err)
	}
	if err = s.AppendWorker(ctx, 1, e, lease.Token, lease.Owner, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAppendWorkerPreservesSubmissionFence(t *testing.T) {
	s, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, s, "worker-job")
	claim := claimSubmission(t, s)
	start := event("start", 1, runtime.TaskStarted)
	start.Data.SubmissionID = claim.Status.ID
	if err := s.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e := event("worker", 2, runtime.WorkerStarted)
	e.WorkerID = "worker"
	for _, credentials := range [][2]string{{"", ""}, {claim.Status.ID, "wrong"}} {
		if err = s.AppendWorker(ctx, 1, e, lease.Token, lease.Owner, credentials[0], credentials[1]); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
			t.Fatal(err)
		}
	}
	if err = s.AppendWorker(ctx, 1, e, lease.Token, lease.Owner, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CancelSubmission(ctx, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	complete := event("complete", 3, runtime.TaskCompleted)
	if err = s.AppendWorker(ctx, 2, complete, lease.Token, lease.Owner, claim.Status.ID, claim.Token); !errors.Is(err, runtime.ErrCancellationRequested) {
		t.Fatal(err)
	}
	if err = s.AppendWorker(ctx, 2, event("cancel", 3, runtime.TaskCanceled), lease.Token, lease.Owner, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
}
