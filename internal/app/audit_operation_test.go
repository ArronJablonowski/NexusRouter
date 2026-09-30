package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func auditOperationSource(t *testing.T) (*Service, evaluation.AuditRequest) {
	t.Helper()
	svc, _ := autoFixture(t)
	result, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "Return exactly a.", Domain: "general"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, evaluation.AuditRequest{Version: 1, IdempotencyKey: "0123456789abcdef", TaskID: result.TaskID, ReviewerModelID: "z"}
}

func auditResponse(t *testing.T, w http.ResponseWriter, r *http.Request, summary string) {
	t.Helper()
	var request struct {
		Model string `json:"model"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "z" {
		t.Error("unexpected audit request")
		return
	}
	audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "general", Verdict: "accept", Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: summary, EvidenceRefs: []string{"candidate"}}}}
	body, _ := json.Marshal(audit)
	fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
}

func TestAuditOperationConcurrentReplayDispatchesOnce(t *testing.T) {
	svc, request := auditOperationSource(t)
	var calls atomic.Int32
	providerStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(providerStarted)
		}
		<-release
		auditResponse(t, w, r, "The literal output matches.")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	type outcome struct {
		status evaluation.AuditStatus
		err    error
	}
	primary := make(chan outcome, 1)
	go func() {
		status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
		primary <- outcome{status, err}
	}()
	<-providerStarted

	const retries = 12
	results := make(chan outcome, retries)
	var wg sync.WaitGroup
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := 0
			status, err := svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
				seen++
				if event.Sequence != 1 {
					t.Errorf("pending replay sequence=%d", event.Sequence)
				}
				return nil
			})
			if seen != 1 {
				t.Errorf("pending replay events=%d", seen)
			}
			results <- outcome{status, err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.status.Status != "pending" {
			t.Fatal("concurrent retry was not a pending replay", result.status, result.err)
		}
	}
	close(release)
	first := <-primary
	if first.err != nil || first.status.Status != "completed" || calls.Load() != 1 {
		t.Fatal("primary audit did not complete exactly once", first.status, first.err, calls.Load())
	}
	var replay []evaluation.AuditEvent
	final, err := svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		replay = append(replay, event)
		return nil
	})
	if err != nil || final.Status != "completed" || len(replay) != 2 || replay[0].Sequence != 1 || replay[1].Sequence != 2 || calls.Load() != 1 {
		t.Fatal("terminal retry was not durable replay", final, replay, err, calls.Load())
	}
}

type auditOperationFactoryProvider struct {
	streams   *atomic.Int32
	streamErr error
}

func (p auditOperationFactoryProvider) Models(context.Context) ([]string, error) {
	return []string{"z"}, nil
}

func (p auditOperationFactoryProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	p.streams.Add(1)
	if p.streamErr != nil {
		return p.streamErr
	}
	audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "general", Verdict: "accept", Confidence: 1, Findings: []evaluation.AuditFinding{{Summary: "factory review", EvidenceRefs: []string{"candidate"}}}}
	body, _ := json.Marshal(audit)
	return emit(providers.Chunk{Text: string(body), Done: true, FinishReason: "stop"})
}

func TestAuditOperationAdmissionPrecedesProviderSetup(t *testing.T) {
	svc, request := auditOperationSource(t)
	var builds, streams atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		if builds.Add(1) == 1 {
			close(buildStarted)
		}
		<-releaseBuild
		return auditOperationFactoryProvider{streams: &streams}, nil
	})

	type outcome struct {
		status evaluation.AuditStatus
		err    error
	}
	primary := make(chan outcome, 1)
	go func() {
		status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
		primary <- outcome{status, err}
	}()
	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("provider setup did not start")
	}

	const retries = 4
	results := make(chan outcome, retries)
	var wg sync.WaitGroup
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
			results <- outcome{status, err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.status.Status != "pending" {
			t.Fatal("retry did not observe durable admission", result.status, result.err)
		}
	}
	if builds.Load() != 1 || streams.Load() != 0 {
		t.Fatal("same-key retry duplicated provider setup", builds.Load(), streams.Load())
	}
	close(releaseBuild)
	result := <-primary
	if result.err != nil || result.status.Status != "completed" || builds.Load() != 1 || streams.Load() != 1 {
		t.Fatal("owning audit did not complete", result.status, result.err, builds.Load(), streams.Load())
	}
}

func TestAuditOperationSetupFailureIsDurablyTerminal(t *testing.T) {
	svc, request := auditOperationSource(t)
	var builds atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, errors.New("private setup failure")
	})
	var events []evaluation.AuditEvent
	status, err := svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, ErrAdmission) || status.Status != "failed" || status.ErrorCode != "review_failed" || len(events) != 2 || builds.Load() != 1 {
		t.Fatal("post-admission setup failure was not terminal", status, events, err, builds.Load())
	}
	status, err = svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "failed" || builds.Load() != 1 {
		t.Fatal("terminal setup failure repeated provider setup", status, err, builds.Load())
	}
}

func TestAuditOperationMalformedAndTimeoutAreDurableFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Service, *atomic.Int32) func()
	}{
		{
			name: "malformed reviewer output",
			configure: func(svc *Service, calls *atomic.Int32) func() {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					_, _ = fmt.Fprintln(w, `{"message":{"content":"private malformed output"},"done":true,"done_reason":"stop"}`)
				}))
				svc.settings.Providers[0].Endpoint = server.URL
				return server.Close
			},
		},
		{
			name: "provider timeout",
			configure: func(svc *Service, calls *atomic.Int32) func() {
				svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
					return auditOperationFactoryProvider{streams: calls, streamErr: context.DeadlineExceeded}, nil
				})
				return func() {}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, request := auditOperationSource(t)
			var calls atomic.Int32
			defer tc.configure(svc, &calls)()

			var events []evaluation.AuditEvent
			status, err := svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
				events = append(events, event)
				return nil
			})
			if !errors.Is(err, ErrAuditOperation) || status.Status != "failed" || status.ErrorCode != "review_failed" || len(events) != 2 || events[0].Status.Status != "pending" || events[1].Status.Status != "failed" || calls.Load() != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal("failed audit lifecycle was not durable and safe", status, events, err, calls.Load())
			}
			status, err = svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
			if err != nil || status.Status != "failed" || status.ErrorCode != "review_failed" || calls.Load() != 1 {
				t.Fatal("failed audit replay repeated inference", status, err, calls.Load())
			}
		})
	}
}

func TestAuditOperationProjectsProviderNativeModelName(t *testing.T) {
	svc, request := auditOperationSource(t)
	for i := range svc.settings.Models {
		if svc.settings.Models[i].ID == request.ReviewerModelID {
			svc.settings.Models[i].Model = "registry/reviewer:latest"
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "registry/reviewer:latest" {
			t.Error("native provider model identity was not preserved", body.Model)
			return
		}
		audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "general", Verdict: "abstain", Findings: []evaluation.AuditFinding{}}
		encoded, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", encoded)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "abstained" || status.EvaluatorModel != "registry/reviewer:latest" || status.ReviewerID != "z" {
		t.Fatal("provider-native identity was not safely projected", status, err)
	}
}

func TestAuditOperationLostAckConflictAndDetachedReplay(t *testing.T) {
	svc, request := auditOperationSource(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		auditResponse(t, w, r, "durable finding")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL

	status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return errors.New("private callback failure") })
	if !errors.Is(err, ErrAuditDelivery) || status.Status != "canceled" || calls.Load() != 0 {
		t.Fatal("failed admission delivery dispatched or leaked", status, err, calls.Load())
	}
	var replay []evaluation.AuditEvent
	status, err = svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		replay = append(replay, event)
		return nil
	})
	if err != nil || status.Status != "canceled" || len(replay) != 2 || calls.Load() != 0 {
		t.Fatal("lost acknowledgement was not replayed", status, replay, err, calls.Load())
	}
	db, openErr := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
	if openErr != nil {
		t.Fatal(openErr)
	}
	attempt, readErr := db.ReviewAttempt(context.Background(), status.ID)
	db.Close()
	encodedAttempt, _ := json.Marshal(attempt)
	if readErr != nil || status.ID == request.IdempotencyKey || attempt.RequestDigest == "" || strings.Contains(string(encodedAttempt), request.IdempotencyKey) {
		t.Fatal("raw idempotency key entered durable identity", attempt, readErr)
	}

	changed := request
	changed.MaxCost = 1
	if _, err := svc.RunAudit(context.Background(), changed, func(evaluation.AuditEvent) error { return nil }); !errors.Is(err, telemetry.ErrConflict) {
		t.Fatal("changed intent did not conflict", err)
	}

	request.IdempotencyKey = "fedcba9876543210"
	status, err = svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "completed" || calls.Load() != 1 {
		t.Fatal("new operation did not complete", status, err, calls.Load())
	}
	_, err = svc.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		if event.Sequence == 2 {
			event.Status.Findings[0].Summary = "caller mutation"
			event.Status.Findings[0].EvidenceRefs[0] = "requirements"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := svc.InspectAudit(context.Background(), request.TaskID, auditOperationID(request.IdempotencyKey))
	if err != nil || inspected.Findings[0].Summary != "durable finding" || inspected.Findings[0].EvidenceRefs[0] != "candidate" {
		t.Fatal("callback mutated durable/public state", inspected, err)
	}
}

func TestAuditOperationCancellationWinsAndCompletionWins(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		svc, request := auditOperationSource(t)
		started := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
		}))
		defer server.Close()
		svc.settings.Providers[0].Endpoint = server.URL
		done := make(chan error, 1)
		go func() {
			status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
			if status.Status != "canceled" {
				done <- fmt.Errorf("run status=%s: %w", status.Status, err)
				return
			}
			done <- err
		}()
		<-started
		canceled, err := svc.CancelAudit(context.Background(), request.TaskID, auditOperationID(request.IdempotencyKey))
		if err != nil || canceled.Status != "canceled" {
			t.Fatal("cancel did not win", canceled, err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal("running call did not return durable cancellation", err)
		}
	})

	t.Run("completion", func(t *testing.T) {
		svc, request := auditOperationSource(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { auditResponse(t, w, r, "complete") }))
		defer server.Close()
		svc.settings.Providers[0].Endpoint = server.URL
		completed, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
		if err != nil || completed.Status != "completed" {
			t.Fatal(completed, err)
		}
		after, err := svc.CancelAudit(context.Background(), request.TaskID, completed.ID)
		if err != nil || after.Status != "completed" || after.AuditID != completed.AuditID {
			t.Fatal("late cancel replaced completion", after, err)
		}
	})
}

func TestAuditOperationRestartInspectionAndReadOnlyBoundaries(t *testing.T) {
	svc, request := auditOperationSource(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		auditResponse(t, w, r, "rotating-secret finding")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	status, err := svc.RunAudit(context.Background(), request, func(evaluation.AuditEvent) error { return nil })
	if err != nil || status.Status != "completed" {
		t.Fatal(status, err)
	}

	restarted, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	var events []evaluation.AuditEvent
	replayed, err := restarted.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || replayed.ID != status.ID || len(events) != 2 || calls.Load() != 1 {
		t.Fatal("restart repeated provider work", replayed, events, err, calls.Load())
	}
	page, err := restarted.ReadAuditEvents(context.Background(), request.TaskID, status.ID, 1)
	if err != nil || page.FromSequence != 1 || page.NextSequence != 2 || len(page.Events) != 1 || page.Events[0].Sequence != 2 {
		t.Fatal("event resume failed", page, err)
	}

	if _, err := restarted.InspectAudit(context.Background(), "other-task", status.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cross-task lookup became an oracle", err)
	}

	restarted.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "rotating-secret"
		}
		return ""
	}
	redacted, err := restarted.InspectAudit(context.Background(), request.TaskID, status.ID)
	if err != nil || redacted.Findings[0].Summary != "[REDACTED] finding" {
		t.Fatal("current secret was not re-redacted", redacted, err)
	}
	restarted.secret = func(string) string { return "z" }
	if _, err := restarted.InspectAudit(context.Background(), request.TaskID, status.ID); !errors.Is(err, ErrAuditOperation) {
		t.Fatal("secret-bearing identity did not fail closed", err)
	}

	missing := svc.settings
	missing.Telemetry.Database = filepath.Join(t.TempDir(), "absent", "audit.db")
	reader, err := NewService(missing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.InspectAudit(context.Background(), request.TaskID, status.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing read-only store returned wrong error", err)
	}
	if _, err := os.Stat(missing.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only inspection created storage", err)
	}
}

func TestAuditOperationRestartDoesNotDispatchStartedAttempt(t *testing.T) {
	svc, request := auditOperationSource(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		auditResponse(t, w, r, "must not run")
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	digest, err := svc.auditRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Read(context.Background(), request.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sourceAttempt string
	for _, event := range events {
		if event.Kind == runtime.TurnStarted {
			sourceAttempt = event.AttemptID
		}
	}
	operation := auditOperationID(request.IdempotencyKey)
	_, created, err := db.AdmitReview(context.Background(), evaluation.ReviewAttempt{Version: 1, ID: operation, TaskID: request.TaskID, AttemptID: sourceAttempt, ReviewerID: "z", EvaluatorModel: "z", EvaluatorProvider: "local", RequestDigest: digest, Status: "started", StartedAt: time.Now().UTC()})
	db.Close()
	if err != nil || !created {
		t.Fatal("fixture admission failed", err)
	}

	restarted, err := NewService(svc.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	status, err := restarted.RunAudit(context.Background(), request, func(event evaluation.AuditEvent) error {
		seen++
		return nil
	})
	if err != nil || status.Status != "pending" || seen != 1 || calls.Load() != 0 {
		t.Fatal("started operation was automatically retried", status, seen, err, calls.Load())
	}
}
