package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestPrepareSummaryDurableBeforeDispatchAndExactReplay(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source history"})
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	key := "summary-preparation-key-0001"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
		if openErr != nil {
			t.Error(openErr)
			return
		}
		defer db.Close()
		operation := summaryPreparationID("operation", key)
		state, readErr := db.ContextCompactionPlan(ctx, operation)
		attempt, attemptErr := db.SummaryAttempt(ctx, summaryPreparationID("attempt", key))
		if readErr != nil || attemptErr != nil || state.Status != sessions.ContextCompactionStarted || state.TerminalAttempt != nil || attempt.Status != "started" {
			t.Errorf("dispatch preceded durable start: state=%+v attempt=%+v errors=%v/%v", state, attempt, readErr, attemptErr)
		}
		fmt.Fprintln(w, `{"message":{"content":"{\"version\":1,\"summary\":{\"decisions\":[\"preserve source\"]}}"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Providers[0].Executable = "/private/bin/summary-provider-sentinel"
	svc.settings.Providers[0].APIKeyEnv = "SUMMARY_TEST_KEY"
	secretValue := "summary-secret-value-sentinel"
	svc.secret = func(name string) string {
		if name == "SUMMARY_TEST_KEY" {
			return secretValue
		}
		return ""
	}

	first, err := svc.PrepareSummary(ctx, key, request)
	if err != nil || first.Validate() != nil || first.Status != sessions.ContextCompactionStarted || first.TerminalAttempt == nil || first.TerminalAttempt.Status != "drafted" || calls.Load() != 1 {
		t.Fatalf("first=%+v err=%v calls=%d", first, err, calls.Load())
	}
	second, err := svc.PrepareSummary(ctx, key, request)
	if err != nil || second.Validate() != nil || !reflect.DeepEqual(first, second) || calls.Load() != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", second, err, calls.Load())
	}
	if first.Start.OperationID != summaryPreparationID("operation", key) || first.Start.RequestID != summaryPreparationID("request", key) || first.Start.AttemptID != summaryPreparationID("attempt", key) ||
		first.Start.Engine.ID != "darwin.default" || first.Start.Tiers.Validate() != nil || string(first.Start.ConfigSnapshot) == "" || string(first.Start.PolicySnapshot) == "" {
		t.Fatal("missing deterministic or frozen preparation evidence", first.Start)
	}
	body, err := json.Marshal(first.Start)
	if err != nil || strings.Contains(string(body), server.URL) || strings.Contains(string(body), svc.settings.Providers[0].Executable) || strings.Contains(string(body), secretValue) {
		t.Fatalf("sensitive provider material entered durable operation: %s", body)
	}
	changed := request
	changed.MaxCost = 1
	if _, err = svc.PrepareSummary(ctx, key, changed); !errors.Is(err, telemetry.ErrConflict) || calls.Load() != 1 {
		t.Fatalf("changed request err=%v calls=%d", err, calls.Load())
	}
	svc.settings.Security.RedactEnv = []string{"NEW_SUMMARY_SECRET"}
	if _, err = svc.PrepareSummary(ctx, key, request); !errors.Is(err, telemetry.ErrConflict) || calls.Load() != 1 {
		t.Fatalf("changed redaction policy err=%v calls=%d", err, calls.Load())
	}
}

func TestPrepareSummaryFailureIsTerminalAndDoesNotRedispatch(t *testing.T) {
	svc, _ := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source history"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	key := "summary-preparation-failure-0001"
	first, err := svc.PrepareSummary(ctx, key, request)
	if err != nil || first.Validate() != nil || first.Status != sessions.ContextCompactionFailed || first.TerminalAttempt == nil || first.TerminalAttempt.Status != "failed" || calls.Load() != 1 {
		body, _ := json.Marshal(first)
		t.Fatalf("first=%s err=%v calls=%d", body, err, calls.Load())
	}
	second, err := svc.PrepareSummary(ctx, key, request)
	if err != nil || !reflect.DeepEqual(first, second) || calls.Load() != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", second, err, calls.Load())
	}
}

func TestPrepareSummaryPendingReplayDoesNotDispatch(t *testing.T) {
	svc, _ := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source history"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		fmt.Fprintln(w, `{"message":{"content":"{\"version\":1,\"summary\":{\"decisions\":[\"done\"]}}"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	key := "summary-preparation-pending-0001"
	type result struct {
		state sessions.ContextCompactionOperationState
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		state, runErr := svc.PrepareSummary(ctx, key, request)
		finished <- result{state, runErr}
	}()
	<-entered
	pending, err := svc.PrepareSummary(ctx, key, request)
	if err != nil || pending.Validate() != nil || pending.Status != sessions.ContextCompactionStarted || pending.TerminalAttempt != nil || calls.Load() != 1 {
		t.Fatalf("pending=%+v err=%v calls=%d", pending, err, calls.Load())
	}
	close(release)
	completed := <-finished
	if completed.err != nil || completed.state.TerminalAttempt == nil || completed.state.TerminalAttempt.Status != "drafted" || calls.Load() != 1 {
		t.Fatalf("completed=%+v err=%v calls=%d", completed.state, completed.err, calls.Load())
	}
}

func TestPrepareSummaryRejectsInvalidKeyBeforeDurableWork(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source history"})
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	if _, err = svc.PrepareSummary(ctx, "short", request); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attempts, err := db.ListSummaryAttempts(ctx, source.TaskID, "", 100)
	if err != nil || len(attempts) != 0 {
		t.Fatal(attempts, err)
	}
}
