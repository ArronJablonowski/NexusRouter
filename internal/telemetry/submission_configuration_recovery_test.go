package telemetry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestRetireConfigurationMismatchQueuedAndExpiredCancellation(t *testing.T) {
	ctx := context.Background()
	db, path := submissionStore(t)
	current := submitDigest("new-config")
	queued := queuedSubmission(t, db, "queued")
	var requestBefore []byte
	if err := db.db.QueryRow(`SELECT request FROM submissions WHERE id=?`, queued.ID).Scan(&requestBefore); err != nil {
		t.Fatal(err)
	}
	changed, err := db.RetireConfigurationMismatch(ctx, queued.ID, current, time.Now().UTC())
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	status, err := db.Submission(ctx, queued.ID)
	if err != nil || status.State != "failed" || status.ErrorCode != "configuration_changed" || status.Result != nil || len(status.TaskIDs) != 0 {
		t.Fatal(status, err)
	}
	stream, err := db.ReadSubmissionStreamPage(ctx, queued.ID, 0, 100)
	if err != nil || stream.Status.State != "failed" || stream.Status.ErrorCode != "configuration_changed" || len(stream.Events) != 0 {
		t.Fatal("retired submission was not inspectable", stream, err)
	}
	history, err := db.RecoveryHistory(ctx, queued.ID)
	if err != nil || len(history) != 1 || history[0].Action != "failed" || history[0].Reason != "configuration_changed" {
		t.Fatal(history, err)
	}
	if changed, err = db.RetireConfigurationMismatch(ctx, queued.ID, current, time.Now().UTC()); err != nil || changed {
		t.Fatal("retirement was not idempotent", changed, err)
	}
	var requestAfter []byte
	if err := db.db.QueryRow(`SELECT request FROM submissions WHERE id=?`, queued.ID).Scan(&requestAfter); err != nil || !reflect.DeepEqual(requestBefore, requestAfter) {
		t.Fatal("retirement changed immutable request", err)
	}

	expired := queuedSubmission(t, db, "expired-undispatched")
	claim := claimSubmission(t, db)
	if claim.Status.ID != expired.ID {
		t.Fatal("fixture claimed wrong submission", claim.Status.ID, expired.ID)
	}
	changed, err = db.RetireConfigurationMismatch(ctx, expired.ID, current, time.Now().Add(2*time.Minute).UTC())
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	status, err = db.Submission(ctx, expired.ID)
	if err != nil || status.State != "failed" || status.ErrorCode != "configuration_changed" || len(status.TaskIDs) != 0 {
		t.Fatal("expired undispatched work was not retired", status, err)
	}

	canceled := queuedSubmission(t, db, "canceled")
	claim = claimSubmission(t, db)
	if claim.Status.ID != canceled.ID {
		t.Fatal("fixture claimed wrong submission", claim.Status.ID, canceled.ID)
	}
	if _, err := db.CancelSubmission(ctx, canceled.ID); err != nil {
		t.Fatal(err)
	}
	changed, err = db.RetireConfigurationMismatch(ctx, canceled.ID, current, time.Now().Add(2*time.Minute).UTC())
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	status, err = db.Submission(ctx, canceled.ID)
	if err != nil || status.State != "canceled" || status.ErrorCode != "canceled" || !status.CancelRequested {
		t.Fatal("cancellation lost precedence", status, err)
	}
	history, err = db.RecoveryHistory(ctx, canceled.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "cancellation_requested" {
		t.Fatal(history, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err = reopened.RecoveryHistory(ctx, queued.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "configuration_changed" {
		t.Fatal("retirement did not survive restart", history, err)
	}
}

func TestRetireConfigurationMismatchLeavesUnsafeOrFreshWorkFenced(t *testing.T) {
	for _, mode := range []string{"same_config", "fresh", "started", "corrupt_request"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, _ := submissionStore(t)
			job := queuedSubmission(t, db, mode)
			current := submitDigest("new-config")
			now := time.Now().Add(2 * time.Minute).UTC()
			if mode != "corrupt_request" {
				claim := claimSubmission(t, db)
				if mode == "same_config" {
					current = claim.Status.ConfigDigest
				}
				if mode == "fresh" {
					now = time.Now().UTC()
				}
				if mode == "started" {
					start := event("started", 1, runtime.TaskStarted)
					start.Data.SubmissionID = job.ID
					if err := db.AppendSubmission(ctx, 0, start, job.ID, claim.Token); err != nil {
						t.Fatal(err)
					}
				}
			} else if _, err := db.db.Exec(`UPDATE submissions SET request='{}' WHERE id=?`, job.ID); err != nil {
				t.Fatal(err)
			}
			changed, err := db.RetireConfigurationMismatch(ctx, job.ID, current, now)
			if mode == "corrupt_request" {
				if changed || !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal("corruption did not fail closed", changed, err)
				}
			} else if err != nil || changed {
				t.Fatal("unsafe work was retired", changed, err)
			}
			status, readErr := db.Submission(ctx, job.ID)
			if readErr != nil || status.State == "failed" || status.State == "canceled" {
				t.Fatal("fenced work was terminalized", status, readErr)
			}
			history, historyErr := db.RecoveryHistory(ctx, job.ID)
			if historyErr != nil || len(history) != 0 {
				t.Fatal(history, historyErr)
			}
		})
	}
}

func TestRetireConfigurationMismatchSerializesWithTaskStart(t *testing.T) {
	for i := 0; i < 20; i++ {
		db, _ := submissionStore(t)
		ctx := context.Background()
		job := queuedSubmission(t, db, "race")
		claim := claimSubmission(t, db)
		start := event("start", 1, runtime.TaskStarted)
		start.Data.SubmissionID = job.ID
		var retired bool
		var retireErr, appendErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			retired, retireErr = db.RetireConfigurationMismatch(ctx, job.ID, submitDigest("new-config"), time.Now().Add(2*time.Minute))
		}()
		go func() {
			defer wg.Done()
			appendErr = db.AppendSubmission(ctx, 0, start, job.ID, claim.Token)
		}()
		wg.Wait()
		if retireErr != nil || retired == (appendErr == nil) {
			t.Fatalf("retirement/task start did not have one winner: %v %v %v", retired, retireErr, appendErr)
		}
		if retired && !errors.Is(appendErr, runtime.ErrExecutionLeaseLost) {
			t.Fatal(appendErr)
		}
		db.Close()
	}
}

func TestRetireConfigurationMismatchPreservesCancellation(t *testing.T) {
	db, _ := submissionStore(t)
	job := queuedSubmission(t, db, "canceled-context")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := db.RetireConfigurationMismatch(ctx, job.ID, submitDigest("new-config"), time.Now().UTC())
	if changed || !errors.Is(err, context.Canceled) {
		t.Fatal(changed, err)
	}
	status, readErr := db.Submission(context.Background(), job.ID)
	if readErr != nil || status.State != "queued" || status.ErrorCode != "" {
		t.Fatal("canceled reconciliation mutated work", status, readErr)
	}
}

func TestConfigurationMismatchCandidatesSkipHistoryAndAdvancePastCorruption(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	for i := 0; i < 300; i++ {
		item := queuedSubmission(t, db, fmt.Sprintf("irrelevant-%04d", i))
		if _, err := db.db.Exec(`UPDATE submissions SET state='succeeded' WHERE id=?`, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	corrupt := queuedSubmission(t, db, "corrupt-candidate")
	if _, err := db.db.Exec(`UPDATE submissions SET config_digest='corrupt' WHERE id=?`, corrupt.ID); err != nil {
		t.Fatal(err)
	}
	valid := queuedSubmission(t, db, "valid-after-corruption")
	current := submitDigest("new-config")
	page, err := db.ConfigurationMismatchCandidatesPage(ctx, current, "", 100, time.Now().UTC())
	if !errors.Is(err, submissions.ErrInvalid) || page.NextCursor == "" || len(page.Items) != 1 || page.Items[0].ID != valid.ID {
		t.Fatal("corruption pinned or hid later stale work", page, err)
	}
	if changed, retireErr := db.RetireConfigurationMismatch(ctx, valid.ID, current, time.Now().UTC()); retireErr != nil || !changed {
		t.Fatal(changed, retireErr)
	}
	if _, err := db.db.Exec(`UPDATE submissions SET config_digest=? WHERE id=?`, submitDigest("config"), corrupt.ID); err != nil {
		t.Fatal(err)
	}
	late := queuedSubmission(t, db, "inserted-after-fence")
	next, err := db.ConfigurationMismatchCandidatesPage(ctx, current, page.NextCursor, 100, time.Now().UTC())
	if err != nil || len(next.Items) != 2 || next.Items[0].ID != corrupt.ID || next.Items[1].ID != late.ID || next.NextCursor != page.NextCursor {
		t.Fatal("completed sweep did not reconsider repair and later insertion", next, err)
	}
}

func TestConfigurationMismatchCandidatesReconsiderNewlyExpiredWork(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	job := queuedSubmission(t, db, "fresh-running")
	claim := claimSubmission(t, db)
	if claim.Status.ID != job.ID {
		t.Fatal(claim.Status.ID, job.ID)
	}
	current := submitDigest("new-config")
	now := time.Now().UTC()
	first, err := db.ConfigurationMismatchCandidatesPage(ctx, current, "", 100, now)
	if err != nil || len(first.Items) != 0 || first.NextCursor == "" {
		t.Fatal("fresh work was selected", first, err)
	}
	second, err := db.ConfigurationMismatchCandidatesPage(ctx, current, first.NextCursor, 100, now.Add(2*time.Minute))
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != job.ID || !second.Items[0].LeaseExpired || second.NextCursor != first.NextCursor {
		t.Fatal("later expiration was not reconsidered", second, err)
	}
	if changed, retireErr := db.RetireConfigurationMismatch(ctx, job.ID, current, now.Add(2*time.Minute)); retireErr != nil || !changed {
		t.Fatal(changed, retireErr)
	}
}
