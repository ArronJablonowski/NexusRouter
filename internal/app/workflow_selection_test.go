package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func selectionAppFixture(t *testing.T) (*Service, []string, *atomic.Int32) {
	t.Helper()
	svc, tasks := skillGenerationAppFixture(t)
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		const draft = `{"version":1,"description":"Selected workflow","tags":["creative"],"steps":["Inspect requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check requirements"]}`
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", draft)
	}))
	t.Cleanup(server.Close)
	svc.settings.Providers[0].Endpoint = server.URL
	return svc, tasks, calls
}

func planAppSelection(t *testing.T, svc *Service, tasks []string) skills.WorkflowSelection {
	t.Helper()
	selection, err := svc.PlanWorkflowSelection(context.Background(), "a", skills.Key{Scope: "project", Name: "workflow"}, "trusted_group", "host_v1", tasks, 0)
	if err != nil || selection.Validate() != nil {
		t.Fatal(selection, err)
	}
	return selection
}

func TestWorkflowSelectionPlansSavedSourcesAndClaimsOnce(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	ctx := context.Background()
	selection := planAppSelection(t, svc, tasks)
	repeated := planAppSelection(t, svc, []string{tasks[1], tasks[0]})
	if !reflect.DeepEqual(selection, repeated) || calls.Load() != 0 {
		t.Fatal("planning changed identity/time or dispatched", selection, repeated, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.WorkflowSelection(ctx, "project", selection.ID)
	if err != nil || !reflect.DeepEqual(saved, selection) {
		t.Fatal("returned selection was not persisted", err)
	}
	attempts, err := db.ListSkillGenerationAttempts(ctx, "project", "", 100)
	if err != nil || len(attempts) != 0 {
		t.Fatal("planning claimed generation", attempts, err)
	}
	a, err := svc.GenerateSkillSelection(ctx, selection.ID, 0)
	if err != nil || a.ID != selection.ID || a.Status != "drafted" || calls.Load() != 1 {
		t.Fatal(a, err, calls.Load())
	}
	if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || calls.Load() != 1 {
		t.Fatal("selection redispatched", err, calls.Load())
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("planning/generation opened skill root", err)
	}
}

func TestWorkflowSelectionRejectsEvidenceRevisionEvenWhenStillAccepted(t *testing.T) {
	svc, tasks, calls := selectionAppFixture(t)
	selection := planAppSelection(t, svc, tasks)
	ctx := context.Background()
	history, err := FeedbackHistory(ctx, svc.settings.Telemetry.Database, tasks[0])
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	if err := ReviseFeedback(ctx, svc.settings.Telemetry.Database, tasks[0], history[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GenerateSkillSelection(ctx, selection.ID, 0); err == nil || calls.Load() != 0 {
		t.Fatal("stale evidence dispatched", err, calls.Load())
	}
	updated := planAppSelection(t, svc, tasks)
	if updated.ID == selection.ID {
		t.Fatal("changed evidence did not change selection identity")
	}
	if _, err := svc.GenerateSkillSelection(ctx, updated.ID, 0); err != nil || calls.Load() != 1 {
		t.Fatal("fresh selection rejected", err, calls.Load())
	}
}

func TestWorkflowSelectionRejectsPolicyDriftBeforeClaim(t *testing.T) {
	for _, change := range []string{"context", "cost-limit", "hardware", "local-policy", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			svc, tasks, calls := selectionAppFixture(t)
			selection := planAppSelection(t, svc, tasks)
			cost := 0.0
			switch change {
			case "context":
				svc.settings.Models[0].ContextTokens++
			case "cost-limit":
				cost = 1
			case "hardware":
				svc.settings.Hardware.MaxRAM--
			case "local-policy":
				svc.settings.Skills.LocalOnly = !svc.settings.Skills.LocalOnly
			case "endpoint":
				svc.settings.Providers[0].Endpoint += "/changed"
			}
			if _, err := svc.GenerateSkillSelection(context.Background(), selection.ID, cost); err == nil || calls.Load() != 0 {
				t.Fatal("policy drift dispatched", err, calls.Load())
			}
			db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			attempts, err := db.ListSkillGenerationAttempts(context.Background(), "project", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("drift claimed attempt", attempts, err)
			}
		})
	}
}

func TestWorkflowSelectionAdmissionCreatesNoLedgerEntries(t *testing.T) {
	for _, mode := range []string{"disabled", "scope", "missing-task", "duplicate-task", "bad-group", "bad-algorithm", "unknown-model", "nan-cost", "secret-group", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			svc, tasks, calls := selectionAppFixture(t)
			ctx := context.Background()
			key, group, algorithm, model, cost := skills.Key{Scope: "project", Name: "workflow"}, "group", "host_v1", "a", 0.0
			switch mode {
			case "disabled":
				svc.settings.Skills.AutoDraft = false
			case "scope":
				key.Scope = "elsewhere"
			case "missing-task":
				tasks[0] = "missing"
			case "duplicate-task":
				tasks[0] = tasks[1]
			case "bad-group":
				group = "unbounded group input"
			case "bad-algorithm":
				algorithm = ""
			case "unknown-model":
				model = "missing"
			case "nan-cost":
				cost = math.NaN()
			case "secret-group":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return group
					}
					return ""
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			selection, err := svc.PlanWorkflowSelection(ctx, model, key, group, algorithm, tasks, cost)
			if err == nil || !reflect.DeepEqual(selection, skills.WorkflowSelection{}) || calls.Load() != 0 {
				t.Fatal("invalid admission", selection, err, calls.Load())
			}
			db, err := telemetry.OpenReadOnly(context.Background(), svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			selections, err := db.ListWorkflowSelections(context.Background(), "project", "", 100)
			if err != nil || len(selections) != 0 {
				t.Fatal("invalid plan saved", selections, err)
			}
		})
	}
}

func TestWorkflowSelectionDoesNotTrustWellFormedStoredProvenance(t *testing.T) {
	for _, field := range []string{"source-digest", "evaluation-digest", "sequence", "session", "privacy"} {
		t.Run(field, func(t *testing.T) {
			svc, tasks, calls := selectionAppFixture(t)
			selection := planAppSelection(t, svc, tasks)
			source := &selection.Sources[0]
			switch field {
			case "source-digest":
				source.SourceDigest = strings.Repeat("a", 64)
			case "evaluation-digest":
				source.EvaluationDigest = strings.Repeat("b", 64)
			case "sequence":
				source.SourceSequence++
			case "session":
				source.SessionID = "another-session"
			case "privacy":
				if source.Privacy == "local_only" {
					source.Privacy = "cloud_allowed"
				} else {
					source.Privacy = "local_only"
				}
			}
			forged, err := skills.NewWorkflowSelection(selection.Key, selection.Group, selection.Algorithm, selection.ModelID, selection.PolicyDigest, selection.Sources, selection.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			db, err := telemetry.Open(context.Background(), svc.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.SaveWorkflowSelection(context.Background(), forged); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GenerateSkillSelection(context.Background(), forged.ID, 0); err == nil || calls.Load() != 0 {
				t.Fatal("untrue provenance dispatched", field, err, calls.Load())
			}
		})
	}
}

func TestWorkflowSelectionFailedGenerationCannotRedispatch(t *testing.T) {
	svc, tasks := skillGenerationAppFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"not a workflow"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	selection := planAppSelection(t, svc, tasks)
	attempt, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0)
	if err == nil || attempt.Status != "failed" || attempt.ID != selection.ID || calls.Load() != 1 {
		t.Fatal("invalid generation was not durably failed", attempt, err, calls.Load())
	}
	if _, err := svc.GenerateSkillSelection(context.Background(), selection.ID, 0); err == nil || calls.Load() != 1 {
		t.Fatal("failed selection redispatched", err, calls.Load())
	}
}
