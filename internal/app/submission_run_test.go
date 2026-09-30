package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func runSubmissionService(t *testing.T, handler http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()
	provider := httptest.NewServer(handler)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "run-submission.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	s, err := NewService(cfg, nil)
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	s.profile = healthProfile
	return s, provider
}

func TestRunSubmissionAlreadyTerminalReturnsIdempotently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	s, provider := runSubmissionService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	})
	defer provider.Close()
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	request := Request{ModelID: "chat", Prompt: "hello"}
	first, err := s.RunSubmission(ctx, "terminal-idempotency-key", request)
	if err != nil || first.State != "succeeded" || first.Result == nil || first.Result.Text != "answer" || calls.Load() != 1 {
		t.Fatal(first, err, calls.Load())
	}
	quick, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	second, err := s.RunSubmission(quick, "terminal-idempotency-key", request)
	if err != nil || second.ID != first.ID || second.State != "succeeded" || second.Result == nil || second.Result.Text != "answer" || calls.Load() != 1 {
		t.Fatal(second, err, calls.Load())
	}
}

func TestRunSubmissionWaitsFromQueuedThroughTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	s, provider := runSubmissionService(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			fmt.Fprintln(w, `{"message":{"content":"detached answer"},"done":true,"done_reason":"stop"}`)
		case <-r.Context().Done():
		}
	})
	defer provider.Close()
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	type outcome struct {
		status submissions.Status
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		status, err := s.RunSubmission(ctx, "queued-terminal-key", Request{ModelID: "chat", Prompt: "wait"})
		done <- outcome{status, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	select {
	case result := <-done:
		t.Fatal("wait returned before terminal state", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil || result.status.State != "succeeded" || result.status.Result == nil || result.status.Result.Text != "detached answer" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("wait did not observe terminal state")
	}
}

func TestRunSubmissionCallerCancellationStopsOnlyWait(t *testing.T) {
	dispatchCtx, stopDispatcher := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopDispatcher()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	s, provider := runSubmissionService(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			fmt.Fprintln(w, `{"message":{"content":"survived waiter cancellation"},"done":true,"done_reason":"stop"}`)
		case <-r.Context().Done():
		}
	})
	defer provider.Close()
	d, err := StartDispatcher(dispatchCtx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	defer releaseProvider()

	waitCtx, cancelWait := context.WithCancel(context.Background())
	type outcome struct {
		status submissions.Status
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		status, err := s.RunSubmission(waitCtx, "cancel-only-wait-key", Request{ModelID: "chat", Prompt: "continue independently"})
		done <- outcome{status, err}
	}()
	select {
	case <-started:
	case <-dispatchCtx.Done():
		t.Fatal("provider did not start")
	}
	cancelWait()
	var interrupted submissions.Status
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.status.State != "queued" && result.status.State != "running" {
			t.Fatal(result)
		}
		interrupted = result.status
	case <-dispatchCtx.Done():
		t.Fatal("canceled waiter did not return")
	}
	releaseProvider()
	terminal := awaitSubmission(t, dispatchCtx, s, interrupted.ID, "succeeded")
	if terminal.CancelRequested || terminal.Result == nil || terminal.Result.Text != "survived waiter cancellation" {
		t.Fatal(terminal)
	}
}

func TestWaitForSubmissionCancellationOverridesDriverInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	initial := submissions.Status{Version: 1, ID: "cancel-driver-race", State: "running"}
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	status, err := waitForSubmission(ctx, initial, ticks, func(context.Context, string) (submissions.Status, error) {
		cancel()
		return submissions.Status{}, errors.New("driver interrupted without wrapping context")
	})
	if !errors.Is(err, context.Canceled) || status.ID != initial.ID || status.State != initial.State {
		t.Fatal(status, err)
	}
	ticks <- time.Now()
	status, err = waitForSubmission(context.Background(), initial, ticks, func(context.Context, string) (submissions.Status, error) {
		return submissions.Status{}, errors.New("independent storage failure")
	})
	if !errors.Is(err, ErrSubmission) || status.ID != initial.ID || status.State != initial.State {
		t.Fatal(status, err)
	}
}

func TestRunSubmissionConflictingKeyAndBodyPreservesOriginal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prompts := make(chan string, 1)
	s, provider := runSubmissionService(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 {
			t.Error("invalid provider request", err, body.Messages)
			return
		}
		prompts <- body.Messages[0].Content
		fmt.Fprintln(w, `{"message":{"content":"original answer"},"done":true,"done_reason":"stop"}`)
	})
	defer provider.Close()

	original := Request{ModelID: "chat", Prompt: "original body"}
	queued, err := s.Submit(ctx, "conflicting-body-key", original)
	if err != nil || queued.State != "queued" {
		t.Fatal(queued, err)
	}
	conflict, err := s.RunSubmission(ctx, "conflicting-body-key", Request{ModelID: "chat", Prompt: "different body"})
	if !errors.Is(err, submissions.ErrConflict) || conflict.ID != "" {
		t.Fatal(conflict, err)
	}
	d, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	terminal := awaitSubmission(t, ctx, s, queued.ID, "succeeded")
	if terminal.Result == nil || terminal.Result.Text != "original answer" {
		t.Fatal(terminal)
	}
	select {
	case prompt := <-prompts:
		if prompt != original.Prompt {
			t.Fatal("conflicting body replaced original", prompt)
		}
	case <-ctx.Done():
		t.Fatal("original request was not dispatched")
	}
}
