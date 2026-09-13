package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func submitDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func submissionStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "submissions.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}
func queuedSubmission(t *testing.T, db *Store, key string) submissions.Status {
	t.Helper()
	return queuedSubmissionBody(t, db, key, []byte(`{"prompt":"private-request"}`))
}
func queuedSubmissionBody(t *testing.T, db *Store, key string, body []byte) submissions.Status {
	t.Helper()
	out, err := db.CreateSubmission(context.Background(), submitDigest(key), submitDigest(string(body)), submitDigest("config"), body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func claimSubmission(t *testing.T, db *Store) submissions.Claim {
	t.Helper()
	claim, err := db.ClaimSubmission(context.Background(), submitDigest("config"), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestSubmissionIdempotenceCapacityAndStatusPrivacy(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	first := queuedSubmission(t, db, "key")
	again := queuedSubmission(t, db, "key")
	if !reflect.DeepEqual(first, again) {
		t.Fatal("retry changed status")
	}
	if _, err := db.CreateSubmission(ctx, submitDigest("key"), submitDigest(`{}`), submitDigest("config"), []byte(`{}`)); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("request conflict lost", err)
	}
	if _, err := db.CreateSubmission(ctx, submitDigest("key"), submitDigest(`{"prompt":"private-request"}`), submitDigest("differentconfig"), []byte(`{"prompt":"private-request"}`)); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("config conflict lost", err)
	}
	for i := 1; i < submissions.MaxQueued; i++ {
		queuedSubmission(t, db, fmt.Sprint(i))
	}
	if _, err := db.CreateSubmission(ctx, submitDigest("full"), submitDigest(`{}`), submitDigest("config"), []byte(`{}`)); !errors.Is(err, submissions.ErrCapacity) {
		t.Fatal("queue overflow accepted", err)
	}
	claim := claimSubmission(t, db)
	if claim.Status.ID != first.ID || len(claim.Request) == 0 || claim.Token == "" {
		t.Fatal("claim did not select oldest", claim.Status)
	}
	encoded, _ := json.Marshal(claim)
	if strings.Contains(string(encoded), claim.Token) || strings.Contains(string(encoded), "private-request") {
		t.Fatal("claim serialized opaque execution data")
	}
	queuedSubmission(t, db, "newafterclaim")
}

func TestSubmissionExactRetryRejectsCorruptStoredRequest(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	created := queuedSubmission(t, db, "corrupt-request")
	if _, err := db.db.Exec(`UPDATE submissions SET request='{"prompt":"changed-private"}' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"prompt":"private-request"}`)
	if _, err := db.CreateSubmission(ctx, submitDigest("corrupt-request"), submitDigest(string(body)), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := db.SubmissionByKey(ctx, submitDigest("corrupt-request"), submitDigest(string(body)), submitDigest("config")); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSubmissionClaimRaceAndLeaseRules(t *testing.T) {
	db, _ := submissionStore(t)
	queuedSubmission(t, db, "one")
	ctx := context.Background()
	ready := make(chan struct{})
	var wg sync.WaitGroup
	claims := make(chan submissions.Claim, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			c, e := db.ClaimSubmission(ctx, submitDigest("config"), time.Now(), time.Minute)
			claims <- c
			errs <- e
		}()
	}
	close(ready)
	wg.Wait()
	close(claims)
	close(errs)
	var claim submissions.Claim
	success, missing := 0, 0
	for c := range claims {
		if c.Token != "" {
			claim = c
		}
	}
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, sql.ErrNoRows) {
			missing++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || missing != 1 {
		t.Fatal(success, missing)
	}
	if _, err := db.RenewSubmission(ctx, claim.Status.ID, "wrong", time.Now(), time.Minute); !errors.Is(err, submissions.ErrLeaseLost) {
		t.Fatal("foreign lease renewed", err)
	}
	if cancel, err := db.RenewSubmission(ctx, claim.Status.ID, claim.Token, time.Now(), time.Minute); err != nil || cancel {
		t.Fatal(cancel, err)
	}
	if _, err := db.CancelSubmission(ctx, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	if cancel, err := db.RenewSubmission(ctx, claim.Status.ID, claim.Token, time.Now(), time.Minute); err != nil || !cancel {
		t.Fatal("cancel signal absent", cancel, err)
	}
	if _, err := db.db.Exec("UPDATE submissions SET lease_expires_at=? WHERE id=?", submissionTime(time.Now().Add(-time.Minute)), claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewSubmission(ctx, claim.Status.ID, claim.Token, time.Now(), time.Minute); !errors.Is(err, submissions.ErrLeaseLost) {
		t.Fatal("expired owner renewed", err)
	}
	status, err := db.Submission(ctx, claim.Status.ID)
	if err != nil || !status.LeaseExpired {
		t.Fatal(status, err)
	}
	if _, err := db.ClaimSubmission(ctx, submitDigest("config"), time.Now(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired claim reclaimed", err)
	}
}

func TestSubmissionClaimCannotPredateCreation(t *testing.T) {
	db, _ := submissionStore(t)
	created := queuedSubmission(t, db, "chronology")
	ttl := time.Minute
	claim, err := db.ClaimSubmission(context.Background(), submitDigest("config"), created.CreatedAt.Add(-time.Second), ttl)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Status.UpdatedAt.Before(claim.Status.CreatedAt) || claim.Status.LeaseExpiresAt == nil ||
		!claim.Status.LeaseExpiresAt.Equal(claim.Status.UpdatedAt.Add(ttl)) {
		t.Fatal("claim chronology is invalid", claim.Status)
	}
}

func TestSubmissionAppendGateAndTerminalAttribution(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "job")
	claim := claimSubmission(t, db)
	start := event("start", 1, runtime.TaskStarted)
	start.Data.SubmissionID = claim.Status.ID
	if err := db.Append(ctx, 0, start); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("generic append bypassed ownership", err)
	}
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, "wrong"); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("foreign append accepted", err)
	}
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	status, err := db.Submission(ctx, claim.Status.ID)
	if err != nil || !reflect.DeepEqual(status.TaskIDs, []string{"task"}) {
		t.Fatal("task linkage lost", status, err)
	}
	if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", &submissions.Result{TaskID: "task", Text: "answer", Turns: 1}); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("running task marked success", err)
	}
	complete := event("completed", 2, runtime.TaskCompleted)
	if err := db.Append(ctx, 1, complete); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("generic follow-up bypassed ownership", err)
	}
	if err := db.AppendSubmission(ctx, 1, complete, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	result := &submissions.Result{TaskID: "task", Text: "answer", Turns: 1}
	invalidCost := math.NaN()
	invalid := *result
	invalid.RouteEstimatedCost = &invalidCost
	if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", &invalid); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("invalid route cost persisted", err)
	}
	cost := .25
	result.RouteEstimatedCost = &cost
	finished, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", result)
	if err != nil || finished.State != "succeeded" || finished.Result.RouteEstimatedCost == nil || *finished.Result.RouteEstimatedCost != cost {
		t.Fatal(finished, err)
	}
	again, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", result)
	if err != nil || !reflect.DeepEqual(finished, again) {
		t.Fatal("finish retry changed result", again, err)
	}
	result.Text = "different"
	if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", result); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("terminal result overwritten", err)
	}
}

func TestSubmissionCancelAndExpiryAllowOwnedCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			start := event("start", 1, runtime.TaskStarted)
			start.Data.SubmissionID = claim.Status.ID
			if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
			want := runtime.ErrCancellationRequested
			if mode == "cancel" {
				if _, err := db.CancelSubmission(ctx, claim.Status.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				want = runtime.ErrExecutionLeaseLost
				if _, err := db.db.Exec("UPDATE submissions SET lease_expires_at=? WHERE id=?", submissionTime(time.Now().Add(-time.Minute)), claim.Status.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
				t.Fatal("exact retry blocked", err)
			}
			if err := db.AppendSubmission(ctx, 1, event("complete", 2, runtime.TaskCompleted), claim.Status.ID, claim.Token); !errors.Is(err, want) {
				t.Fatal("normal append not rejected", err)
			}
			cleanup := event("tool", 2, runtime.ToolCompleted)
			cleanup.TurnID = "turn"
			cleanup.Data = runtime.Data{ToolCallID: "call", ToolName: "read", Effect: runtime.NoEffect}
			if err := db.AppendSubmission(ctx, 1, cleanup, claim.Status.ID, "wrong"); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
				t.Fatal("foreign cleanup accepted", err)
			}
			if err := db.AppendSubmission(ctx, 1, cleanup, claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
			if err := db.AppendSubmission(ctx, 2, event("canceled", 3, runtime.TaskCanceled), claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
			if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "canceled", "canceled", nil); err != nil {
				t.Fatal("expired cleanup failed", err)
			}
		})
	}
}

func TestSubmissionRestartBoundsAndQueuedCancellation(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	status := queuedSubmission(t, db, "job")
	for _, body := range [][]byte{nil, []byte("not JSON"), []byte(`{"x":"` + strings.Repeat("a", submissions.MaxRequestBytes) + `"}`)} {
		if _, err := db.CreateSubmission(ctx, submitDigest("bad"), submitDigest("req"), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatal("invalid body accepted", err)
		}
	}
	canceled, err := db.CancelSubmission(ctx, status.ID)
	if err != nil || canceled.State != "canceled" || !canceled.CancelRequested {
		t.Fatal(canceled, err)
	}
	again, err := db.CancelSubmission(ctx, status.ID)
	if err != nil || !reflect.DeepEqual(canceled, again) {
		t.Fatal("cancellation not idempotent")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	restored, err := ro.Submission(ctx, status.ID)
	if err != nil || !reflect.DeepEqual(canceled, restored) {
		t.Fatal("restart changed submission", restored, err)
	}
	if _, err := ro.Submission(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := ro.CancelSubmission(ctx, status.ID); err == nil {
		t.Fatal("readonly cancellation wrote")
	}
}

func TestSubmissionSuccessRequiresFreshUncanceledOwner(t *testing.T) {
	for _, mode := range []string{"expire", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			start := event("start", 1, runtime.TaskStarted)
			start.Data.SubmissionID = claim.Status.ID
			if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
			if err := db.AppendSubmission(ctx, 1, event("complete", 2, runtime.TaskCompleted), claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				if _, err := db.CancelSubmission(ctx, claim.Status.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.db.Exec("UPDATE submissions SET lease_expires_at=? WHERE id=?", submissionTime(time.Now().Add(-time.Minute)), claim.Status.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", &submissions.Result{TaskID: "task", Turns: 1}); !errors.Is(err, submissions.ErrLeaseLost) {
				t.Fatal("stale success accepted", err)
			}
			if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "failed", "interrupted", nil); err != nil {
				t.Fatal("owned terminal cleanup rejected", err)
			}
		})
	}
}

func TestSubmissionRejectsDigestMismatchAndOwnsRequest(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	body := []byte(`{"prompt":"original"}`)
	if _, err := db.CreateSubmission(ctx, submitDigest("bad"), submitDigest("mismatch"), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("mismatched digest accepted", err)
	}
	if _, err := db.CreateSubmission(ctx, submitDigest("good"), submitDigest(string(body)), submitDigest("config"), body); err != nil {
		t.Fatal(err)
	}
	body[0] = 'x'
	claim := claimSubmission(t, db)
	if string(claim.Request) != `{"prompt":"original"}` {
		t.Fatal("request aliased caller bytes")
	}
}
