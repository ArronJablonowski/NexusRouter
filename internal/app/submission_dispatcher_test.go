package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func awaitSubmission(t *testing.T, ctx context.Context, s *Service, id, state string) submissions.Status {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := s.SubmissionStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == state {
			return status
		}
		select {
		case <-ctx.Done():
			t.Fatal("submission did not reach", state, status)
		case <-ticker.C:
		}
	}
}

func TestDispatcherQueuedRestartExecutesIdempotentSubmissionOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Workers.Max = 1
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "dispatch.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "hello"})
	if err != nil || b.ID != a.ID {
		t.Fatal(b, err)
	}
	// New service/dispatcher must discover the persisted queued body, without
	// relying on a goroutine or request object retained by the intake service.
	restarted, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err = StartDispatcher(ctx, restarted)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	status := awaitSubmission(t, ctx, restarted, a.ID, "succeeded")
	if status.Result == nil || status.Result.Text != "answer" || len(status.TaskIDs) != 1 || calls.Load() != 1 {
		t.Fatal(status, calls.Load())
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherCancellationStopsBlockedProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		defer close(stopped)
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	defer func() { cancel(); provider.Close() }()
	cfg := config.Defaults()
	cfg.Workers.Max = 1
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "cancel.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider not started")
	}
	if _, err := s.CancelSubmission(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("provider not canceled")
	}
	status := awaitSubmission(t, ctx, s, a.ID, "canceled")
	if !status.CancelRequested || status.Result != nil && status.Result.Text != "" {
		t.Fatal(status)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherAdmissionFailureHasNoInventedTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := submissionService(t)
	a, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "unknown", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	status := awaitSubmission(t, ctx, s, a.ID, "failed")
	if status.Result != nil || len(status.TaskIDs) != 0 {
		t.Fatal("invented task result", status)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherCancelBeforeExecutionSlotHasNoInventedTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := submissionService(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"unexpected"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	s.settings.Mode = "local_only"
	s.settings.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	s.settings.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	s.settings.Workers.Max = 1
	configured, err := NewService(s.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	s = configured
	s.profile = healthProfile
	s.execution = make(chan struct{}, 1)
	s.execution <- struct{}{}
	a, err := s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	awaitSubmission(t, ctx, s, a.ID, "running")
	if _, err := s.CancelSubmission(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	<-s.execution
	status := awaitSubmission(t, ctx, s, a.ID, "canceled")
	if status.Result != nil || len(status.TaskIDs) != 0 || calls.Load() != 0 {
		t.Fatal(status)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}
