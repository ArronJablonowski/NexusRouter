package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func recoveryFixture(t *testing.T) (*Service, *telemetry.Store, *atomic.Int32) {
	t.Helper()
	s := submissionService(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(provider.Close)
	s.settings.Workers.Max = 1
	s.settings.Mode = "local_only"
	s.settings.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	s.settings.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	configured, err := NewService(s.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	s = configured
	s.profile = healthProfile
	db, err := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return s, db, &calls
}

func recoveryClaim(t *testing.T, s *Service, db *telemetry.Store) submissions.Claim {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func expireRecoveryClaim(t *testing.T, s *Service, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE submissions SET lease_expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryStartupOnlyExecutesUndispatchedClaims(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, db, calls := recoveryFixture(t)
			claim := recoveryClaim(t, s, db)
			if dispatched {
				e := runtime.Event{Version: 1, ID: "started", TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: claim.Status.ID, ModelID: "fixture", ProviderID: "local", Messages: []providers.Message{{Role: "user", Content: "hello"}}}}
				if err := db.AppendSubmission(ctx, 0, e, claim.Status.ID, claim.Token); err != nil {
					t.Fatal(err)
				}
			}
			expireRecoveryClaim(t, s, claim.Status.ID)
			awaitID := claim.Status.ID
			if dispatched {
				control, err := s.Submit(ctx, "0123456789abcdef-control", Request{ModelID: "chat", Prompt: "control"})
				if err != nil {
					t.Fatal(err)
				}
				awaitID = control.ID
			}
			d, err := StartDispatcher(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			awaitSubmission(t, ctx, s, awaitID, "succeeded")
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatal("repeated provider execution", calls.Load())
			}
			if dispatched {
				status, err := s.SubmissionStatus(ctx, claim.Status.ID)
				if err != nil || status.State != "failed" || status.LeaseExpired || status.Result == nil || status.Result.TaskID != "task" || status.Result.Text != "" {
					t.Fatal(status, err)
				}
			}
		})
	}
}

func TestRecoveryFencesWorkerWaitingBeforeTaskStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, db, calls := recoveryFixture(t)
	claim := recoveryClaim(t, s, db)
	s.execution = make(chan struct{}, 1)
	s.execution <- struct{}{}
	d := &Dispatcher{db: db}
	finished := make(chan struct{})
	go func() { defer close(finished); d.execute(ctx, s, claim) }()
	expireRecoveryClaim(t, s, claim.Status.ID)
	if _, err := d.recoverPage(ctx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	<-s.execution
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("old worker did not stop")
	}
	if d.err != nil || calls.Load() != 0 {
		t.Fatal("fenced worker dispatched or poisoned supervisor", d.err, calls.Load())
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "queued" || len(status.TaskIDs) != 0 {
		t.Fatal(status, err)
	}
	active, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	awaitSubmission(t, ctx, s, claim.Status.ID, "succeeded")
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestRecoveryAdvancesOneBoundedPageAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, db, _ := recoveryFixture(t)
	for i := 0; i < 101; i++ {
		key := fmt.Sprintf("key-%016d", i)
		keyDigest, requestDigest, body, err := s.submissionPayload(key, Request{ModelID: "chat", Prompt: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		// This test exercises bounded recovery pagination, not the public
		// submission adapter. Reuse the fixture's already-open store so schema
		// validation and WAL setup do not consume the test's recovery deadline
		// once per seeded row.
		if _, err := db.CreateSubmission(ctx, keyDigest, requestDigest, s.submissionConfigDigest(), body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ClaimSubmission(ctx, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := sql.Open("sqlite", s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "UPDATE submissions SET lease_expires_at=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{db: db}
	after, err := d.recoverPage(ctx, s.submissionConfigDigest(), "")
	if err != nil || after == "" {
		t.Fatal(after, err)
	}
	page, err := db.ListSubmissions(ctx, submissions.ListOptions{State: "running", Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(len(page.Items), err)
	}
	after, err = d.recoverPage(ctx, s.submissionConfigDigest(), after)
	if err != nil || after != "" {
		t.Fatal(after, err)
	}
	page, err = db.ListSubmissions(ctx, submissions.ListOptions{State: "running", Limit: 100})
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
}
