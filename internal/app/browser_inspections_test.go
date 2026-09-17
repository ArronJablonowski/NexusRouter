package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	contract "github.com/ArronJablonowski/DarwinRouter/webui"
)

func appendInspectionEvent(t *testing.T, db *telemetry.Store, event runtime.Event) {
	t.Helper()
	if err := db.Append(context.Background(), event.Sequence-1, event); err != nil {
		t.Fatal(err)
	}
}

func inspectionEvent(task string, sequence int64, kind runtime.Kind, at time.Time) runtime.Event {
	return runtime.Event{Version: 1, ID: "event_" + task + "_" + string(rune('a'+sequence)), TaskID: task, SessionID: "session",
		CorrelationID: task, Sequence: sequence, Time: at.Add(time.Duration(sequence) * time.Second).UTC(), Kind: kind}
}

func TestBrowserToolInspectionPairsLifecycleAndOmitsPayload(t *testing.T) {
	service := submissionService(t)
	db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(100, 0).UTC()
	start := inspectionEvent("task", 1, runtime.TaskStarted, base)
	appendInspectionEvent(t, db, start)
	toolStart := inspectionEvent("task", 2, runtime.ToolStarted, base)
	toolStart.TurnID, toolStart.AttemptID = "turn", "attempt"
	toolStart.Data.ToolCallID, toolStart.Data.ToolName = "call_one", "read_file"
	toolStart.Data.ToolBehavior, toolStart.Data.Effect = runtime.BehaviorReadOnly, runtime.UncertainEffect
	appendInspectionEvent(t, db, toolStart)
	toolEnd := inspectionEvent("task", 3, runtime.ToolCompleted, base)
	toolEnd.TurnID, toolEnd.AttemptID = "turn", "attempt"
	toolEnd.Data.ToolCallID, toolEnd.Data.ToolName = "call_one", "read_file"
	toolEnd.Data.ToolBehavior, toolEnd.Data.Effect, toolEnd.Data.Text = runtime.BehaviorReadOnly, runtime.NoEffect, "private-result-token"
	appendInspectionEvent(t, db, toolEnd)
	appendInspectionEvent(t, db, inspectionEvent("task", 4, runtime.TaskCompleted, base))
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	page, err := service.BrowserTools(context.Background(), "task", "", 1)
	body, _ := json.Marshal(page)
	if err != nil || page.Validate() != nil || len(page.Tools) != 1 || page.Tools[0].State != "completed" || page.Tools[0].Permission != "unknown" || page.Tools[0].CompletionSequence == nil || strings.Contains(string(body), "private-result-token") {
		t.Fatal(page, err, string(body))
	}
	restarted := &Service{settings: service.settings}
	replayed, err := restarted.BrowserTools(context.Background(), "task", "", 1)
	if err != nil || replayed.Validate() != nil || replayed.Tools[0].CallID != "call_one" {
		t.Fatal(replayed, err)
	}
}

func TestBrowserToolInspectionFailsClosedOnOrphanCompletion(t *testing.T) {
	service := submissionService(t)
	db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(100, 0).UTC()
	appendInspectionEvent(t, db, inspectionEvent("task", 1, runtime.TaskStarted, base))
	tool := inspectionEvent("task", 2, runtime.ToolCompleted, base)
	tool.TurnID, tool.AttemptID = "turn", "attempt"
	tool.Data.ToolCallID, tool.Data.ToolName, tool.Data.Effect = "call_one", "read_file", runtime.NoEffect
	appendInspectionEvent(t, db, tool)
	db.Close()
	if _, err = service.BrowserTools(context.Background(), "task", "", 10); !errors.Is(err, ErrInspection) {
		t.Fatal("orphan tool completion released", err)
	}
}

func TestBrowserModelsResourcesAndUsagePreserveAvailability(t *testing.T) {
	service := submissionService(t)
	service.settings.Providers = []config.Provider{{ID: "provider", Kind: "ollama"}}
	service.settings.Models = []config.Model{{ID: "model", Provider: "provider", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 4096, RAMBytes: 10}}
	report := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Status: "healthy", Ready: true, Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "model", ID: "model", Status: "healthy", Code: "available"},
	}}
	models, err := service.BrowserModels(context.Background(), report)
	if err != nil || models.Validate() != nil || models.Availability != contract.Available || len(models.Models) != 1 || models.Models[0].Health != "healthy" {
		t.Fatal(models, err)
	}
	service.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), CPUs: 4, TotalRAM: 100, AvailableRAM: 50}, nil
	}
	resource := service.BrowserResources(context.Background())
	if resource.Validate() != nil || resource.Availability != contract.Available || resource.CPUs == nil || *resource.CPUs != 4 {
		t.Fatal(resource)
	}
	service.profile = func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, resources.ErrProfile }
	if unavailable := service.BrowserResources(context.Background()); unavailable.Validate() != nil || unavailable.Availability != contract.Unavailable {
		t.Fatal(unavailable)
	}
	seedUsageTask(t, service, "usage_task", "usage_session")
	usage, err := service.BrowserTaskUsage(context.Background(), "usage_task")
	if err != nil || usage.Validate() != nil || usage.Availability != contract.Available || usage.Usage == nil || usage.Usage.Routed.Records != 0 || usage.Usage.Auxiliary.Records != 0 {
		t.Fatal(usage, err)
	}
}

func TestBrowserModelsDiscoversInstalledLocalModelsAndDeduplicatesAliases(t *testing.T) {
	digest := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		_, _ = fmt.Fprintf(writer, `{"models":[`+
			`{"name":"fixture:latest","modified_at":"2026-09-17T12:00:00Z","size":1024,"digest":%q,"details":{"family":"fixture","parameter_size":"1B","quantization_level":"Q4"}},`+
			`{"name":"fixture:alias","modified_at":"2026-09-17T12:00:00Z","size":1024,"digest":%q,"details":{"family":"fixture","parameter_size":"1B","quantization_level":"Q4"}}]}`, digest, digest)
	}))
	defer server.Close()
	service := submissionService(t)
	service.settings.Providers = []config.Provider{{ID: "ollama", Kind: "ollama", Endpoint: server.URL}}
	service.settings.Models = []config.Model{{ID: "configured", Provider: "ollama", Model: "fixture:latest", Locality: "local", Capabilities: []string{"chat"}, RAMBytes: 1}}
	report := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Status: "healthy", Ready: true, Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "provider", ID: "ollama", Status: "healthy", Code: "available"}, {Component: "model", ID: "configured", Status: "healthy", Code: "available"},
	}}
	page, err := service.BrowserModels(context.Background(), report)
	if err != nil || page.Validate() != nil || len(page.Models) != 2 || page.LocalTotalBytes == nil || *page.LocalTotalBytes != 1024 || page.LocalUnknownSizeCount != 0 {
		t.Fatalf("unexpected inventory page: %+v, %v", page, err)
	}
	if !page.Models[0].Configured || !page.Models[0].Installed || !page.Models[0].Usable || page.Models[0].SizeBytes == nil || *page.Models[0].SizeBytes != 1024 {
		t.Fatalf("configured model not enriched: %+v", page.Models[0])
	}
	if page.Models[1].Configured || !page.Models[1].Installed || page.Models[1].Usable || page.Models[1].Model != "fixture:alias" {
		t.Fatalf("unconfigured install not represented: %+v", page.Models[1])
	}
}

func TestBrowserHealthUnavailableDoesNotInventMeasurements(t *testing.T) {
	got := BrowserHealth(health.Report{}, errors.New("probe failed"))
	if got.Validate() != nil || got.Availability != contract.Unavailable || got.Ready != nil || len(got.Checks) != 0 {
		t.Fatal(got)
	}
}

func TestBrowserRouteSeparatesExplicitAbsenceFromCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		service := submissionService(t)
		db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
		if err != nil {
			t.Fatal(err)
		}
		base := time.Unix(100, 0).UTC()
		appendInspectionEvent(t, db, inspectionEvent("task", 1, runtime.TaskStarted, base))
		if corrupt {
			route := inspectionEvent("task", 2, runtime.RouteSelected, base)
			route.RouteID, route.Data.ModelID, route.Data.ProviderID = "route", "model", "provider"
			appendInspectionEvent(t, db, route)
			appendInspectionEvent(t, db, inspectionEvent("task", 3, runtime.TaskCompleted, base))
		} else {
			appendInspectionEvent(t, db, inspectionEvent("task", 2, runtime.TaskCompleted, base))
		}
		db.Close()
		got, err := service.BrowserRoute(context.Background(), "task")
		if corrupt {
			if !errors.Is(err, ErrInspection) {
				t.Fatal("corrupt route represented as absent", got, err)
			}
		} else if err != nil || got.Validate() != nil || got.Availability != contract.Unavailable {
			t.Fatal("explicit route absence rejected", got, err)
		}
	}
}

func TestBrowserAuditsProjectsAutomaticAuditProvenance(t *testing.T) {
	service := submissionService(t)
	db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(100, 0).UTC()
	appendInspectionEvent(t, db, inspectionEvent("task", 1, runtime.TaskStarted, base))
	turnStart := inspectionEvent("task", 2, runtime.TurnStarted, base)
	turnStart.TurnID, turnStart.AttemptID = "turn", "attempt"
	appendInspectionEvent(t, db, turnStart)
	turnEnd := inspectionEvent("task", 3, runtime.TurnCompleted, base)
	turnEnd.TurnID, turnEnd.AttemptID = "turn", "attempt"
	appendInspectionEvent(t, db, turnEnd)
	appendInspectionEvent(t, db, inspectionEvent("task", 4, runtime.TaskCompleted, base))
	attempt := evaluation.ReviewAttempt{Version: 1, ID: "review", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "review-model", EvaluatorProvider: "provider", Status: "started", StartedAt: base.Add(5 * time.Second)}
	if err = db.BeginReview(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	record := evaluation.AuditRecord{Version: 1, ID: "audit", TaskID: "task", AttemptID: "attempt", EvaluatorModel: "review-model", EvaluatorProvider: "provider",
		Audit:        evaluation.Audit{Version: 1, EvaluatorID: "orchestrator", RubricVersion: "v1", Domain: "code", Verdict: "abstain", Findings: []evaluation.AuditFinding{}},
		EvidenceRefs: []string{"candidate"}, Elapsed: time.Second, Time: base.Add(6 * time.Second)}
	attempt.Status, attempt.AuditID, attempt.FinishedAt = "completed", "audit", base.Add(7*time.Second)
	if err = db.CompleteReview(context.Background(), attempt, record); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := service.BrowserAudits(context.Background(), "task", "", 10)
	if err != nil || page.Validate() != nil || len(page.Audits) != 1 || page.Audits[0].ReviewerID != "orchestrator" || page.Audits[0].Status != "abstained" {
		t.Fatal(page, err)
	}
}
