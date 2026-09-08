package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

const sdkAuditSecret = "sdk-audit-secret-fixture"
const sdkAuditNativeReviewer = "family/reviewer:model"

type sdkAuditFixture struct {
	client, reopened *sdk.Client
	configPath       string
	database         string
	candidateCalls   atomic.Int32
	reviewerCalls    atomic.Int32
}

func newSDKAuditFixture(t *testing.T, handler func(http.ResponseWriter, *http.Request, string)) *sdkAuditFixture {
	t.Helper()
	fixture := &sdkAuditFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model == sdkAuditNativeReviewer {
			fixture.reviewerCalls.Add(1)
		} else {
			fixture.candidateCalls.Add(1)
		}
		handler(w, r, body.Model)
	}))
	t.Cleanup(server.Close)
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "audit.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "SDK_AUDIT_SECRET"}}
	zero := 0.0
	cfg.Models = []config.Model{
		{ID: "candidate", Provider: "local", Model: "candidate-model", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero},
		{ID: "reviewer", Provider: "local", Model: sdkAuditNativeReviewer, Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero},
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.configPath = filepath.Join(t.TempDir(), "config.yaml")
	fixture.database = cfg.Telemetry.Database
	if err = os.WriteFile(fixture.configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	options := sdk.ConfigOptions{
		ProjectFile: fixture.configPath,
		LookupSecret: func(name string) string {
			if name == "SDK_AUDIT_SECRET" {
				return sdkAuditSecret
			}
			return ""
		},
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }),
	}
	fixture.client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	fixture.reopened, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func sdkAuditHandler(w http.ResponseWriter, _ *http.Request, model string) {
	text := "candidate output that must not appear in audit metadata"
	if model == sdkAuditNativeReviewer {
		review := evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "reject", Confidence: .9, Findings: []evaluation.AuditFinding{{Summary: sdkAuditSecret + " missing error check", EvidenceRefs: []string{"candidate_execution"}}}}
		body, _ := json.Marshal(review)
		text = string(body)
	}
	fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
}

func runSDKAuditCandidate(t *testing.T, fixture *sdkAuditFixture, prompt string) sdk.Result {
	t.Helper()
	out, err := fixture.client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "candidate", Prompt: prompt, Domain: "code"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSDKAuditRunReplayInspectAndDetachedEvents(t *testing.T) {
	fixture := newSDKAuditFixture(t, sdkAuditHandler)
	const prompt = "private SDK audit prompt"
	candidate := runSDKAuditCandidate(t, fixture, prompt)
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-audit-idempotency-01", TaskID: candidate.TaskID, ReviewerModelID: "reviewer", MaxCost: 0}
	var delivered []sdk.AuditEvent
	status, err := fixture.client.RunAudit(context.Background(), request, func(event sdk.AuditEvent) error {
		delivered = append(delivered, event)
		// The callback owns a detached projection and cannot corrupt the result.
		event.Status.EvidencePrecedence[0] = evaluation.LLMJudge
		if len(event.Status.Findings) > 0 {
			event.Status.Findings[0].Summary = "mutated"
			event.Status.Findings[0].EvidenceRefs[0] = "mutated"
		}
		return nil
	})
	if err != nil || status.Validate() != nil || status.Status != "rejected" || status.ID == "" || len(delivered) != 2 || delivered[0].Status.Status != "pending" || delivered[1].Status.Status != "rejected" || fixture.candidateCalls.Load() != 1 || fixture.reviewerCalls.Load() != 1 {
		t.Fatalf("status=%+v events=%+v candidate=%d reviewer=%d err=%v", status, delivered, fixture.candidateCalls.Load(), fixture.reviewerCalls.Load(), err)
	}
	if len(status.Findings) != 1 || status.Findings[0].Summary == "mutated" || strings.Contains(status.Findings[0].Summary, sdkAuditSecret) || status.EvidencePrecedence[0] != evaluation.Deterministic {
		t.Fatal("callback aliased or secret escaped", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil || strings.Contains(string(encoded), prompt) || strings.Contains(string(encoded), "candidate output") || strings.Contains(string(encoded), sdkAuditSecret) {
		t.Fatal("sensitive audit status", string(encoded), err)
	}

	inspected, err := fixture.reopened.InspectAudit(context.Background(), candidate.TaskID, status.ID)
	if err != nil || inspected.Validate() != nil || inspected.Status != status.Status || inspected.AuditID != status.AuditID {
		t.Fatal(inspected, err)
	}
	inspected.Findings[0].Summary = "corrupt"
	inspected.EvidenceRefs[0] = "corrupt"
	inspected.EvidencePrecedence[0] = evaluation.LLMJudge
	page, err := fixture.reopened.ReadAuditEvents(context.Background(), candidate.TaskID, status.ID, 0)
	if err != nil || page.Validate() != nil || len(page.Events) != 2 || page.NextSequence != 2 || page.HasMore {
		var validations []error
		for _, event := range page.Events {
			validations = append(validations, event.Validate(), event.Status.Validate())
		}
		t.Fatal(page, err, page.Validate(), validations)
	}
	page.Events[1].Status.Findings[0].Summary = "corrupt"
	page.Events[1].Status.EvidenceRefs[0] = "corrupt"
	fresh, err := fixture.client.ReadAuditEvents(context.Background(), candidate.TaskID, status.ID, 0)
	if err != nil || fresh.Validate() != nil || fresh.Events[1].Status.Findings[0].Summary == "corrupt" || fresh.Events[1].Status.EvidenceRefs[0] == "corrupt" || fresh.Events[1].Status.EvidencePrecedence[0] != evaluation.Deterministic {
		t.Fatal("inspection projection aliased", fresh, err)
	}

	var replay []sdk.AuditEvent
	replayed, err := fixture.reopened.RunAudit(context.Background(), request, func(event sdk.AuditEvent) error {
		replay = append(replay, event)
		return nil
	})
	if err != nil || replayed.Status != "rejected" || replayed.ID != status.ID || len(replay) != 2 || fixture.reviewerCalls.Load() != 1 {
		t.Fatal(replayed, replay, fixture.reviewerCalls.Load(), err)
	}
	for _, path := range []string{fixture.database, fixture.database + "-wal"} {
		body, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), request.IdempotencyKey) || strings.Contains(string(body), sdkAuditSecret) {
			t.Fatal("raw key or credential persisted", path)
		}
	}
}

func TestSDKAuditGuardsCallbackFailureAndCancel(t *testing.T) {
	fixture := newSDKAuditFixture(t, sdkAuditHandler)
	candidate := runSDKAuditCandidate(t, fixture, "callback candidate")
	request := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-audit-callback-01", TaskID: candidate.TaskID, ReviewerModelID: "reviewer"}
	privateErr := errors.New("private callback failure " + sdkAuditSecret)
	failed, err := fixture.client.RunAudit(context.Background(), request, func(sdk.AuditEvent) error { return privateErr })
	if !errors.Is(err, sdk.ErrAuditDelivery) || strings.Contains(err.Error(), "private") || failed.Status != "canceled" || fixture.reviewerCalls.Load() != 0 {
		t.Fatal(failed, fixture.reviewerCalls.Load(), err)
	}
	inspected, err := fixture.reopened.InspectAudit(context.Background(), candidate.TaskID, failed.ID)
	if err != nil || inspected.Status != "canceled" || inspected.ErrorCode != "canceled" {
		t.Fatal(inspected, err)
	}

	second := runSDKAuditCandidate(t, fixture, "cancel candidate")
	cancelRequest := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-audit-cancel-01", TaskID: second.TaskID, ReviewerModelID: "reviewer"}
	pending := make(chan sdk.AuditEvent, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	var runStatus sdk.AuditStatus
	var runErr error
	go func() {
		defer close(finished)
		runStatus, runErr = fixture.client.RunAudit(context.Background(), cancelRequest, func(event sdk.AuditEvent) error {
			if event.Sequence == 1 {
				pending <- event
				<-release
			}
			return nil
		})
	}()
	var admitted sdk.AuditEvent
	select {
	case admitted = <-pending:
	case <-time.After(5 * time.Second):
		t.Fatal("audit admission not emitted")
	}
	canceled, err := fixture.reopened.CancelAudit(context.Background(), second.TaskID, admitted.AuditID)
	if err != nil || canceled.Status != "canceled" || canceled.ErrorCode != "canceled" {
		t.Fatal(canceled, err)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled audit did not join")
	}
	if runStatus.Status != "canceled" || runErr != nil || fixture.candidateCalls.Load() != 2 || fixture.reviewerCalls.Load() != 0 {
		t.Fatal(runStatus, fixture.candidateCalls.Load(), fixture.reviewerCalls.Load(), runErr)
	}
}

func TestSDKAuditStrictInputAndContextGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	valid := sdk.AuditRequest{Version: 1, IdempotencyKey: "sdk-audit-valid-key", TaskID: "task", ReviewerModelID: "reviewer"}
	emit := func(sdk.AuditEvent) error { return nil }
	for _, absent := range []*sdk.Client{nil, {}} {
		if _, err = absent.RunAudit(context.Background(), valid, emit); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
		if _, err = absent.InspectAudit(context.Background(), "task", strings.Repeat("a", 64)); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	badRequests := []sdk.AuditRequest{
		{},
		{Version: 2, IdempotencyKey: "key", TaskID: "task", ReviewerModelID: "reviewer"},
		{Version: 1, IdempotencyKey: " bad", TaskID: "task", ReviewerModelID: "reviewer"},
		{Version: 1, IdempotencyKey: "key", TaskID: "", ReviewerModelID: "reviewer"},
		{Version: 1, IdempotencyKey: "key", TaskID: "task", ReviewerModelID: "", MaxCost: 0},
		{Version: 1, IdempotencyKey: "key", TaskID: "task", ReviewerModelID: "reviewer", MaxCost: -1},
		{Version: 1, IdempotencyKey: "key", TaskID: "task", ReviewerModelID: "reviewer", MaxCost: math.NaN()},
		{Version: 1, IdempotencyKey: "key", TaskID: "task", ReviewerModelID: "reviewer", MaxCost: math.Inf(1)},
	}
	for _, request := range badRequests {
		if out, callErr := client.RunAudit(context.Background(), request, emit); !errors.Is(callErr, sdk.ErrAdmission) || out.Version != 1 {
			t.Fatal(request, out, callErr)
		}
	}
	if _, err = client.RunAudit(context.Background(), valid, nil); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	operation := strings.Repeat("a", 64)
	for _, ctx := range []context.Context{nil, canceledCtx} {
		_, runErr := client.RunAudit(ctx, valid, emit)
		_, inspectErr := client.InspectAudit(ctx, "task", operation)
		_, cancelErr := client.CancelAudit(ctx, "task", operation)
		_, replayErr := client.ReadAuditEvents(ctx, "task", operation, 0)
		for _, callErr := range []error{runErr, inspectErr, cancelErr, replayErr} {
			if callErr == nil {
				t.Fatal("context accepted")
			}
			if ctx == canceledCtx && !errors.Is(callErr, context.Canceled) {
				t.Fatal("context cancellation identity lost", callErr)
			}
		}
	}
	for _, bad := range [][2]string{{"", operation}, {" bad-task", operation}, {"task", "short"}, {"task", strings.Repeat("G", 64)}} {
		if _, err = client.InspectAudit(context.Background(), bad[0], bad[1]); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(bad, err)
		}
		if _, err = client.CancelAudit(context.Background(), bad[0], bad[1]); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(bad, err)
		}
	}
	if _, err = client.ReadAuditEvents(context.Background(), "task", operation, -1); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guarded audit call created storage", err)
	}
}
