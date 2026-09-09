package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestDispatcherRetiresOldConfigurationAndContinuesCurrentWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	old, _, calls := recoveryFixture(t)
	stale, err := old.Submit(ctx, "old-configuration-key", Request{ModelID: "chat", Prompt: "must not execute"})
	if err != nil {
		t.Fatal(err)
	}
	settings := old.settings
	settings.Hardware.MaxRAM--
	current, err := NewService(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	current.profile = healthProfile
	dispatcher, err := StartDispatcher(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	retired := awaitSubmission(t, ctx, current, stale.ID, "failed")
	if retired.ErrorCode != "configuration_changed" || retired.Result != nil || len(retired.TaskIDs) != 0 || calls.Load() != 0 {
		t.Fatal("old configuration executed or lacked disposition", retired, calls.Load())
	}
	fresh, err := current.Submit(ctx, "new-configuration-key", Request{ModelID: "chat", Prompt: "execute current"})
	if err != nil {
		t.Fatal(err)
	}
	completed := awaitSubmission(t, ctx, current, fresh.ID, "succeeded")
	if completed.Result == nil || completed.Result.Text != "answer" || calls.Load() != 1 {
		t.Fatal("current configuration did not remain serviceable", completed, calls.Load())
	}
	history, err := current.SubmissionRecoveries(ctx, stale.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "configuration_changed" {
		t.Fatal(history, err)
	}
}

func TestConfigurationReconciliationIsBoundedAndInsertionFenced(t *testing.T) {
	ctx := context.Background()
	s := submissionService(t)
	for i := 0; i < 101; i++ {
		if _, err := s.Submit(ctx, fmt.Sprintf("old-config-key-%04d", i), Request{ModelID: "fixture", Prompt: "old"}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &Dispatcher{db: db}
	current := s.submissionConfigDigest()
	// Use a distinct valid digest without changing the stored generation.
	current = submissionDigest([]byte(current + "-new"))
	after, err := d.reconcileConfigurationPage(ctx, current, "")
	if err != nil || after == "" {
		t.Fatal("first bounded page", after, err)
	}
	firstAfter := after
	if failed := countConfigurationSubmissions(t, db, "failed"); failed != 100 {
		t.Fatal("first page was not bounded", failed)
	}
	after, err = d.reconcileConfigurationPage(ctx, current, after)
	if err != nil || after == "" || after == firstAfter {
		t.Fatal("second bounded page", after, err)
	}
	if failed := countConfigurationSubmissions(t, db, "failed"); failed != 101 {
		t.Fatal("second page did not complete", failed)
	}
	if next, err := d.reconcileConfigurationPage(ctx, current, after); err != nil || next != after {
		t.Fatal("completed sweep did not retain canonical reset", next, err)
	}
}

func TestConfigurationReconciliationRecoversOldInterruptedHistoryWithoutExecution(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	started := runtime.Event{
		Version: 1, ID: "old-generation-start", TaskID: "old-generation-task", SessionID: "old-generation-task",
		CorrelationID: "old-generation-task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{SubmissionID: claim.Status.ID, ModelID: "fixture", ProviderID: "local", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "hello"}}},
	}
	if err := db.AppendSubmission(ctx, 0, started, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	expireRecoveryClaim(t, s, claim.Status.ID)
	var delivered []runtime.Event
	d := &Dispatcher{
		db:                 db,
		eventSink:          runtime.EventSinkFunc(func(_ context.Context, event runtime.Event) error { delivered = append(delivered, event); return nil }),
		eventSinkSequencer: &configuredSinkSequencer{},
		lifecycle:          context.Background(),
	}
	current := submissionDigest([]byte(s.submissionConfigDigest() + "-new"))
	if after, err := d.reconcileConfigurationPage(ctx, current, ""); err != nil || after == "" {
		t.Fatal(after, err)
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "failed" || status.ErrorCode != "execution_failed" || status.Result == nil || status.Result.TaskID != started.TaskID || calls.Load() != 0 {
		t.Fatal(status, err, calls.Load())
	}
	page, err := db.ReadEventPage(ctx, started.TaskID, 1, 10)
	if err != nil || len(delivered) != 1 || len(page.Events) != 1 || !reflect.DeepEqual(delivered[0], page.Events[0]) {
		t.Fatal("obsolete-configuration recovery did not deliver its exact durable terminal", delivered, page, err)
	}
	history, err := s.SubmissionRecoveries(ctx, claim.Status.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "interrupted_model" {
		t.Fatal(history, err)
	}
}

func TestConfigurationReconciliationProjectsOldTerminalHistoryWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	request, err := decodeSubmission(claim.Request)
	if err != nil {
		t.Fatal(err)
	}
	request.submissionID, request.submissionToken = claim.Status.ID, claim.Token
	result, err := s.Run(ctx, request)
	if err != nil || result.TaskID == "" || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	expireRecoveryClaim(t, s, claim.Status.ID)
	d := &Dispatcher{db: db}
	current := submissionDigest([]byte(s.submissionConfigDigest() + "-new"))
	if after, err := d.reconcileConfigurationPage(ctx, current, ""); err != nil || after == "" {
		t.Fatal(after, err)
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "succeeded" || status.Result == nil || status.Result.TaskID != result.TaskID || status.Result.Text != result.Text || calls.Load() != 1 {
		t.Fatal(status, err, calls.Load())
	}
	history, err := s.SubmissionRecoveries(ctx, claim.Status.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "terminal_history" {
		t.Fatal(history, err)
	}
}

func TestConfigurationReconciliationAdvancesPastCorruptRequest(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	corrupt, err := s.Submit(ctx, "corrupt-old-generation", Request{ModelID: "chat", Prompt: "corrupt"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := s.Submit(ctx, "valid-old-generation", Request{ModelID: "chat", Prompt: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	var originalRequest []byte
	if err = raw.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, corrupt.ID).Scan(&originalRequest); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE submissions SET request='{}' WHERE id=?`, corrupt.ID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: db}
	current := submissionDigest([]byte(s.submissionConfigDigest() + "-new"))
	after, err := d.reconcileConfigurationPage(ctx, current, "")
	if !errors.Is(err, submissions.ErrInvalid) || after == "" {
		t.Fatal("corruption was not reported with an advanced cursor", after, err)
	}
	corruptStatus, corruptErr := s.SubmissionStatus(ctx, corrupt.ID)
	laterStatus, laterErr := s.SubmissionStatus(ctx, later.ID)
	if corruptErr != nil || laterErr != nil || corruptStatus.State != "queued" || laterStatus.State != "failed" || laterStatus.ErrorCode != "configuration_changed" || calls.Load() != 0 {
		t.Fatal(corruptStatus, corruptErr, laterStatus, laterErr, calls.Load())
	}
	raw, err = sql.Open("sqlite", s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE submissions SET request=? WHERE id=?`, originalRequest, corrupt.ID); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := d.reconcileConfigurationPage(ctx, current, after)
	if err != nil || next != after {
		t.Fatal("repaired candidate was not reconsidered", next, err)
	}
	corruptStatus, err = s.SubmissionStatus(ctx, corrupt.ID)
	if err != nil || corruptStatus.State != "failed" || corruptStatus.ErrorCode != "configuration_changed" || calls.Load() != 0 {
		t.Fatal(corruptStatus, err, calls.Load())
	}
}

func TestConfigurationReconciliationReconsidersLaterExpiration(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	d := &Dispatcher{db: db}
	current := submissionDigest([]byte(s.submissionConfigDigest() + "-new"))
	after, err := d.reconcileConfigurationPage(ctx, current, "")
	if err != nil || after == "" {
		t.Fatal(after, err)
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "running" || status.LeaseExpired || calls.Load() != 0 {
		t.Fatal("fresh old-generation lease was changed", status, err, calls.Load())
	}
	expireRecoveryClaim(t, s, claim.Status.ID)
	next, err := d.reconcileConfigurationPage(ctx, current, after)
	if err != nil || next != after {
		t.Fatal(next, err)
	}
	status, err = s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "failed" || status.ErrorCode != "configuration_changed" || len(status.TaskIDs) != 0 || calls.Load() != 0 {
		t.Fatal("expired old-generation lease was not retired", status, err, calls.Load())
	}
}

func countConfigurationSubmissions(t *testing.T, db *telemetry.Store, state string) int {
	t.Helper()
	ctx := context.Background()
	after, count := "", 0
	for {
		page, err := db.ListSubmissions(ctx, submissions.ListOptions{State: state, After: after, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		count += len(page.Items)
		if !page.HasMore {
			return count
		}
		after = page.NextCursor
	}
}
