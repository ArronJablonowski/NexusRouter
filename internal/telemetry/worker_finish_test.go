package telemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestFinishWorkerAtomicRetryAndFences(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_owner", "wrong_token", "writer", "expired_success", "expired_failed", "expired_cancel", "legacy_terminal", "event_rollback", "release_rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, path := leaseStore(t)
			ctx := context.Background()
			lease, err := s.AcquireLease(ctx, "task", "owner", "scope", mode == "writer", time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			e := event("finish", 2, runtime.TaskCompleted)
			e.WorkerID = "owner"
			if mode == "expired_failed" {
				e.Kind = runtime.TaskFailed
			}
			if mode == "expired_cancel" {
				e.Kind = runtime.TaskCanceled
			}
			token, owner := lease.Token, lease.Owner
			switch mode {
			case "wrong_owner":
				owner = "other"
			case "wrong_token":
				token = "other"
			case "expired_success", "expired_failed", "expired_cancel":
				_, err = s.db.Exec(`UPDATE resource_leases SET expires=1`)
			case "legacy_terminal":
				err = s.AppendWorker(ctx, 1, e, token, owner, "", "")
			case "event_rollback":
				_, err = s.db.Exec(`CREATE TRIGGER refuse_terminal BEFORE INSERT ON events WHEN NEW.sequence=2 BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			case "release_rollback":
				_, err = s.db.Exec(`CREATE TRIGGER refuse_release BEFORE UPDATE OF released ON resource_leases BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.FinishLeased(ctx, 1, e, token, owner)
			success := mode == "valid" || mode == "expired_cancel"
			if success && err != nil || !success && err == nil {
				t.Fatal(mode, err)
			}
			reader, err := OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			events, err := reader.Read(ctx, "task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if success || mode == "legacy_terminal" {
				want = 2
			}
			if len(events) != want {
				t.Fatal(len(events), want)
			}
			var released int
			if err = reader.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if success && released != 1 || !success && released != 0 {
				t.Fatal("non-atomic release", released)
			}
			if success {
				if err = s.FinishLeased(ctx, 1, e, token, owner); err != nil {
					t.Fatal("retry", err)
				}
				if err = s.FinishLeased(ctx, 1, e, "forged", owner); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
					t.Fatal("forged retry", err)
				}
				changed := e
				changed.Data.Text = "changed"
				if err = s.FinishLeased(ctx, 1, changed, token, owner); err == nil {
					t.Fatal("changed retry")
				}
				_, err = s.db.Exec(`UPDATE resource_leases SET owner='replacement' WHERE token=?`, token)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.FinishLeased(ctx, 1, e, token, owner); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
					t.Fatal("reassigned retry", err)
				}
			}
		})
	}
}

func TestFinishWorkerSubmissionFence(t *testing.T) {
	s, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, s, "finish-job")
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
	e := event("finish", 2, runtime.TaskCompleted)
	e.WorkerID = "owner"
	for _, cred := range [][2]string{{"", ""}, {claim.Status.ID, "wrong"}} {
		if err = s.FinishWorker(ctx, 1, e, lease.Token, lease.Owner, cred[0], cred[1]); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
			t.Fatal(err)
		}
	}
	if _, err = s.CancelSubmission(ctx, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorker(ctx, 1, e, lease.Token, lease.Owner, claim.Status.ID, claim.Token); !errors.Is(err, runtime.ErrCancellationRequested) {
		t.Fatal(err)
	}
	e.Kind = runtime.TaskCanceled
	if err = s.FinishWorker(ctx, 1, e, lease.Token, lease.Owner, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
}

func TestFinishWorkerRejectsAmbiguousHistoricalLease(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, released := range []int{0, 1} {
			t.Run(fmt.Sprintf("committed_%v_other_released_%d", committed, released), func(t *testing.T) {
				s, _ := leaseStore(t)
				ctx := context.Background()
				lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				e := event("finish", 2, runtime.TaskCompleted)
				e.WorkerID = "owner"
				if committed {
					if err = s.FinishLeased(ctx, 1, e, lease.Token, lease.Owner); err != nil {
						t.Fatal(err)
					}
				}
				_, err = s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES('other-token','task','owner','scope',0,?,?)`, time.Now().Add(time.Minute).UnixNano(), released)
				if err != nil {
					t.Fatal(err)
				}
				for _, token := range []string{lease.Token, "other-token"} {
					if err = s.FinishLeased(ctx, 1, e, token, lease.Owner); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
						t.Fatal("ambiguous finish accepted", err)
					}
				}
				events, err := s.Read(ctx, "task", 0, 100)
				want := 1
				if committed {
					want = 2
				}
				if err != nil || len(events) != want {
					t.Fatal(events, err)
				}
				var actual int
				if err = s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if committed && actual != 1 || !committed && actual != 0 {
					t.Fatal("mutated release", actual)
				}
			})
		}
	}
}
