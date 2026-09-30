package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestRecoveryStartupReconstructsTerminalHistoryWithoutExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	r, err := decodeSubmission(claim.Request)
	if err != nil {
		t.Fatal(err)
	}
	r.submissionID, r.submissionToken = claim.Status.ID, claim.Token
	out, err := s.Run(ctx, r)
	if err != nil || out.TaskID == "" || out.Text != "answer" {
		t.Fatal(out, err)
	}
	before, err := db.Read(ctx, out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "running" || status.Result != nil || len(status.TaskIDs) != 1 {
		t.Fatal("simulated crash must leave terminal task with unfinished ledger", status, err)
	}
	// Only the fixture's lease clock is changed: all task history came from Run.
	expireRecoveryClaim(t, s, claim.Status.ID)
	d := &Dispatcher{db: db}
	if _, err := d.recoverPage(ctx, strings.Repeat("0", 64), ""); err != nil {
		t.Fatal(err)
	}
	status, err = s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "running" {
		t.Fatal("mismatched configuration recovered a task", status, err)
	}
	restarted, err := NewService(s.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	active, err := StartDispatcher(ctx, restarted)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	status = awaitSubmission(t, ctx, restarted, claim.Status.ID, "succeeded")
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || status.Result == nil || status.Result.TaskID != out.TaskID || status.Result.Text != out.Text || !reflect.DeepEqual(status.TaskIDs, []string{out.TaskID}) {
		t.Fatal("recovery repeated execution or lost durable result", calls.Load(), status)
	}
	after, err := db.Read(ctx, out.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed durable task history", err)
	}
	history, err := db.RecoveryHistory(ctx, claim.Status.ID)
	if err != nil || len(history) != 1 || history[0].Action != "succeeded" || history[0].Reason != "terminal_history" {
		t.Fatal(history, err)
	}
	if _, err := d.recoverPage(ctx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	again, err := db.RecoveryHistory(ctx, claim.Status.ID)
	if err != nil || !reflect.DeepEqual(history, again) || calls.Load() != 1 {
		t.Fatal("recovery was not idempotent", again, err)
	}
}

func TestRecoveryClosesModelOnlyStartedHistoryWithoutExecution(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	e := runtime.Event{Version: 1, ID: "partial-start", TaskID: "partial", SessionID: "partial", CorrelationID: "partial", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: claim.Status.ID, ModelID: "fixture", ProviderID: "local", Messages: []providers.Message{{Role: "user", Content: "hello"}}}}
	if err := db.AppendSubmission(ctx, 0, e, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	expireRecoveryClaim(t, s, claim.Status.ID)
	d := &Dispatcher{db: db}
	if _, err := d.recoverPage(ctx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "failed" || status.LeaseExpired || status.Result == nil || status.Result.TaskID != e.TaskID || status.Result.Text != "" || calls.Load() != 0 {
		t.Fatal(status, err, calls.Load())
	}
	history, err := db.RecoveryHistory(ctx, claim.Status.ID)
	if err != nil || len(history) != 1 || history[0].Action != "failed" || history[0].Reason != "interrupted_model" {
		t.Fatal(history, err)
	}
	events, err := db.Read(ctx, e.TaskID, 0, 100)
	if err != nil || len(events) != 2 || !reflect.DeepEqual(events[0], e) || events[1].Kind != runtime.TaskFailed || events[1].Data.Code != "interrupted_model" {
		t.Fatal("recovery changed the prefix or omitted its terminal receipt", events, err)
	}
}
