package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTransientClaimErrorsDoNotRequireSupervisorInspection(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, context.DeadlineExceeded, context.Canceled} {
		if !transientClaimError(err) {
			t.Fatalf("expected transient claim error: %v", err)
		}
	}
	if transientClaimError(errors.New("storage failure")) {
		t.Fatal("storage failure classified as transient")
	}
}

func TestDispatcherHealthLifecycleAndStalls(t *testing.T) {
	now := time.Now()
	d := &Dispatcher{configuredWorkers: 2, healthNow: func() time.Time { return now }}
	check := func(status, code string) {
		t.Helper()
		got := d.Health()
		if err := got.Validate(); err != nil {
			t.Fatalf("invalid health check: %v", err)
		}
		if got.Status != status || got.Code != code {
			t.Fatalf("health %+v want %s %s", got, status, code)
		}
	}
	check("unknown", "supervisor_starting")
	d.supervisorStarted(0)
	d.supervisorStarted(-1)
	check("unknown", "supervisor_starting")
	d.supervisorStarted(1)
	check("healthy", "supervisor_ok")
	now = now.Add(16 * time.Second)
	d.supervisorHeartbeat(0)
	d.supervisorHeartbeat(-1)
	check("degraded", "supervisor_stalled")
	d.supervisorHeartbeat(1)
	check("healthy", "supervisor_ok")
	d.supervisorStopped(1)
	check("degraded", "supervisor_stalled")
	d.recordError()
	check("degraded", "supervisor_error")
	d.closing = true
	check("unavailable", "supervisor_stopping")
	d.closed = true
	check("unavailable", "supervisor_stopped")
}

func TestDispatcherHealthRenewalDuringBlockedProvider(t *testing.T) {
	s, db, _ := recoveryFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	s.settings.Providers[0].Endpoint = server.URL
	claim := recoveryClaim(t, s, db)
	now := time.Now()
	d := &Dispatcher{db: db, configuredWorkers: 1, renewInterval: time.Millisecond, healthNow: func() time.Time { return now }}
	d.supervisorStarted(0)
	d.supervisorStarted(-1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); d.executeWorker(ctx, s, claim, 0) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider not entered")
	}
	d.mu.Lock()
	now = now.Add(16 * time.Second)
	d.mu.Unlock()
	d.supervisorHeartbeat(-1)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for d.Health().Status != "healthy" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("inflight worker heartbeat stalled")
		}
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("worker did not finish")
	}
	if d.Health().Status != "healthy" {
		t.Fatal(d.Health())
	}
}

func TestDispatcherHealthCloseJoins(t *testing.T) {
	s, _, _ := recoveryFixture(t)
	d, err := StartDispatcher(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if got := d.Health(); got.Status != "unavailable" || got.Code != "supervisor_stopped" {
		t.Fatal(got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reconcilerAlive {
		t.Fatal("reconciler survived close")
	}
	for _, alive := range d.workerAlive {
		if alive {
			t.Fatal("worker survived close")
		}
	}
}
