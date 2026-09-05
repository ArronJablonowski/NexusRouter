package app

import (
	"context"
	"testing"
	"time"

	"darwinrouter/resources"
)

func TestSubmissionPressureTimeoutAndCancellationBeforeDispatch(t *testing.T) {
	for _, cancelSubmission := range []bool{false, true} {
		name := "timeout"
		if cancelSubmission {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, db, calls := recoveryFixture(t)
			s.settings.Hardware.LocalPressurePolicy = "wait"
			s.settings.Hardware.LocalQueueTimeout = "100ms"
			if cancelSubmission {
				s.settings.Hardware.LocalQueueTimeout = "2s"
			}
			release, err := s.reserveExplicit(ctx, s.settings.Models[0])
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			profiled := make(chan struct{}, 1)
			s.profile = func(context.Context) (resources.Snapshot, error) {
				select {
				case profiled <- struct{}{}:
				default:
				}
				return healthProfile(ctx)
			}
			claim := recoveryClaim(t, s, db)
			d := &Dispatcher{db: db, renewInterval: 10 * time.Millisecond}
			done := make(chan struct{})
			go func() { defer close(done); d.execute(ctx, s, claim) }()
			select {
			case <-profiled:
			case <-ctx.Done():
				t.Fatal("claim never reached pressure admission")
			}
			if cancelSubmission {
				status, err := s.CancelSubmission(ctx, claim.Status.ID)
				if err != nil || !status.CancelRequested || len(status.TaskIDs) != 0 {
					t.Fatal(status, err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("waiting claim did not finish")
			}
			status, err := db.Submission(ctx, claim.Status.ID)
			want := "failed"
			if cancelSubmission {
				want = "canceled"
			}
			if err != nil || status.State != want || status.Result != nil || len(status.TaskIDs) != 0 || calls.Load() != 0 || d.err != nil {
				t.Fatal("incorrect waiting claim outcome", status, err, calls.Load(), d.err)
			}
			// The same queued request was finalized; no replacement submission
			// or model task was created by admission polling.
			metrics, err := db.Metrics(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range metrics.Groups {
				if group.Name == "tasks" {
					for _, count := range group.Counts {
						if count.Value != 0 {
							t.Fatal(count)
						}
					}
				}
			}
		})
	}
}
