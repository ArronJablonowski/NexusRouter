package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func delegateSafetyExecutor(t *testing.T, db *telemetry.Store, journal runtime.Journal, run delegateRunner) scopedDelegateTestExecutor {
	t.Helper()
	cfg := config.Defaults()
	cfg.Workers.DelegateMaxCalls = 1
	registry := &tools.Registry{}
	if err := registerDelegate(registry, db, journal, cfg, "parent", "session", "", true, run); err != nil {
		t.Fatal(err)
	}
	return scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{{Tool: "delegate", Scope: "delegation", Decision: tools.Allow}}}}}
}
func delegateSafetyCall(raw string) providers.ToolCall {
	return providers.ToolCall{ID: "call", Name: "delegate", Arguments: json.RawMessage(raw)}
}

func TestDelegateSafetyWorkCancellationJoinsAndSuppressesOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entered := make(chan string, 1)
	joined := make(chan struct{})
	executor := delegateSafetyExecutor(t, db, db, func(ctx context.Context, prompt, validation, parent string, local bool) (Result, error) {
		defer close(joined)
		if prompt != "request" || validation != "" || !local {
			t.Error("scope changed")
		}
		entered <- parent
		<-ctx.Done()
		// Even an uncooperative success return after cancellation must not escape.
		return Result{TaskID: "child", Text: "private late answer"}, nil
	})
	type outcome struct {
		result runtime.ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, err := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"request","validation":"text"}`))
		done <- outcome{result, err}
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("delegate did not join")
		}
	}()
	var work string
	select {
	case work = <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = db.RequestCancellation(ctx, work); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out.err != nil || out.result.Effect != runtime.NoEffect || !strings.Contains(out.result.Content, "delegate_unavailable_or_rejected") || strings.Contains(out.result.Content, "private") {
			t.Fatal(out)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-joined:
	default:
		t.Fatal("callback was not joined")
	}
	page, err := db.ReadEventPage(ctx, work, 0, 100)
	if err != nil || page.Validate() != nil || page.State != "canceled" {
		t.Fatal(page, err)
	}
	if page.Events[0].Data.ParentTaskID != "parent" || page.Events[0].CorrelationID != work {
		t.Fatal("broken work linkage", page.Events[0])
	}
}

func TestDelegateSafetySchemaAndParentCallBudget(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls atomic.Int32
	executor := delegateSafetyExecutor(t, db, db, func(context.Context, string, string, string, bool) (Result, error) {
		calls.Add(1)
		return Result{TaskID: "child", Text: "accepted answer"}, nil
	})
	for _, raw := range []string{`{"prompt":"a","prompt":"b","validation":"text"}`, `{"prompt":"a","validation":"text","scope":"untrusted"}`, `{"prompt":"a","validation":"execute"}`, `{"prompt":1,"validation":"text"}`} {
		if _, err := executor.Execute(ctx, delegateSafetyCall(raw)); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
	first, err := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"request","validation":"text"}`))
	if err != nil || !strings.Contains(first.Content, "accepted answer") || calls.Load() != 1 {
		t.Fatal(first, err)
	}
	second, err := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"request","validation":"text"}`))
	if err != nil || second.Effect != runtime.NoEffect || !strings.Contains(second.Content, "delegate_unavailable_or_rejected") || calls.Load() != 1 {
		t.Fatal(second, err)
	}
}

type delegateFailJournal struct{ calls int }

func (j *delegateFailJournal) Append(context.Context, int64, runtime.Event) error {
	j.calls++
	return errors.New("private journal detail")
}
func TestDelegateSafetyPersistenceFailurePreventsCallback(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	journal := &delegateFailJournal{}
	called := false
	executor := delegateSafetyExecutor(t, db, journal, func(context.Context, string, string, string, bool) (Result, error) {
		called = true
		return Result{}, nil
	})
	out, err := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"request","validation":"text"}`))
	if called || journal.calls != 1 || err != nil || out.Effect != runtime.NoEffect || strings.Contains(out.Content, "private") || !strings.Contains(out.Content, "delegate_unavailable_or_rejected") {
		t.Fatal(called, journal.calls, out, err)
	}
}

type delegateFenceJournal struct {
	runtime.Journal
	event runtime.Event
	err   error
}

func (j *delegateFenceJournal) Append(ctx context.Context, seq int64, event runtime.Event) error {
	j.event = event
	j.err = j.Journal.Append(ctx, seq, event)
	return j.err
}

func TestDelegateSafetyStaleSubmissionCannotDispatch(t *testing.T) {
	ctx := context.Background()
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "fenced.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	body := []byte(`{"prompt":"request"}`)
	status, err := db.CreateSubmission(ctx, digest("key"), digest(string(body)), digest("config"), body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, digest("config"), time.Now(), time.Minute)
	if err != nil || claim.Status.ID != status.ID {
		t.Fatal(claim, err)
	}
	journal := &delegateFenceJournal{Journal: redactingJournal{db: db, submissionID: status.ID, submissionToken: "invalid-owner-token"}}
	cfg := config.Defaults()
	registry := &tools.Registry{}
	called := false
	if err = registerDelegate(registry, db, journal, cfg, "parent", "session", status.ID, true, func(context.Context, string, string, string, bool) (Result, error) {
		called = true
		return Result{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{{Tool: "delegate", Scope: "delegation", Decision: tools.Allow}}}}}
	out, err := executor.Execute(ctx, delegateSafetyCall(`{"prompt":"request","validation":"text"}`))
	if err != nil || called || out.Effect != runtime.NoEffect || !strings.Contains(out.Content, "delegate_unavailable_or_rejected") {
		t.Fatal(called, out, err)
	}
	if journal.event.Kind != runtime.TaskStarted || journal.event.Data.SubmissionID != status.ID || !errors.Is(journal.err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("wrong fencing boundary", journal.event, journal.err)
	}
	events, err := db.Read(ctx, journal.event.TaskID, 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatal("fenced work persisted", events, err)
	}
	current, err := db.Submission(ctx, status.ID)
	if err != nil || len(current.TaskIDs) != 0 || current.State != "running" {
		t.Fatal(current, err)
	}
}
