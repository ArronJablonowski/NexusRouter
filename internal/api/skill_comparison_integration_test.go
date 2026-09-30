package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillComparisonHTTPReadOnlyStoredEvidence(t *testing.T) {
	ctx := context.Background()
	input, fixture := comparisonAPIFixture(t)
	input.Tasks = []string{"task-00"}
	var inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { inference.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Skills.Root = filepath.Join(t.TempDir(), "absent-catalog")
	cfg.Skills.Scope = "project"
	cfg.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: input.ModelID, Model: fixture.Policy.Execution.Model, Provider: "provider", Locality: "local", ContextTokens: 4096, RAMBytes: 1, Capabilities: []string{"creative"}}}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: "event-" + string(kind), TaskID: "task-00", SessionID: "session", CorrelationID: "task-00", Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		switch kind {
		case runtime.TaskStarted:
			e.Data = runtime.Data{ModelID: fixture.Policy.Execution.Model, ProviderID: "provider", Domain: input.Domain, Profile: input.Profile, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "PRIVATE_SKILL_BODY"}}, SkillContext: &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: input.Name, Version: input.BaselineVersion, Digest: fixture.Baseline.Digest}}}}
		case runtime.TurnStarted:
			e.Data = runtime.Data{ModelID: fixture.Policy.Execution.Model, ProviderID: "provider"}
		case runtime.TurnCompleted:
			e.Data = runtime.Data{Text: "PRIVATE_MODEL_OUTPUT", FinishReason: "stop"}
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	record := evaluation.Record{Version: 1, ID: "quality", TaskID: "task-00", AttemptID: "attempt", Key: fixture.Policy.Execution, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "feedback", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(101, 0).UTC()}
	if err := db.RecordEvaluation(ctx, record); err != nil {
		t.Fatal(err)
	}
	var collide atomic.Bool
	svc, err := app.NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			if collide.Load() {
				return input.Name
			}
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.CompareSkillOutcomes = svc.CompareSkillOutcomes
	handler, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	call := func(want int) skills.ComparisonReport {
		t.Helper()
		body, _ := json.Marshal(input)
		req, _ := http.NewRequest("POST", server.URL+"/v1/skills/comparison", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || response.StatusCode != want || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), token) {
			t.Fatal(response.StatusCode, string(raw), err)
		}
		var out skills.ComparisonReport
		if want == 200 && json.Unmarshal(raw, &out) != nil {
			t.Fatal("invalid JSON")
		}
		return out
	}
	before, err := db.Read(ctx, "task-00", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := call(200)
	if got.Validate() != nil || got.Sampled != 1 || got.Baseline.Samples != 1 || got.Baseline.Accepted != 1 || got.Candidate.Samples != 0 || got.Status != "insufficient_evidence" || !got.AdvisoryOnly {
		t.Fatal(got)
	}
	next := record
	next.ID = "correction"
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "corrected", Passed: false}}
	if err := db.SupersedeEvaluation(ctx, record.ID, next); err != nil {
		t.Fatal(err)
	}
	corrected := call(200)
	if corrected.Baseline.Accepted != 0 || corrected.EvidenceDigest == got.EvidenceDigest {
		t.Fatal("comparison ignored current feedback")
	}
	collide.Store(true)
	call(503)
	after, err := db.Read(ctx, "task-00", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || inference.Load() != 0 {
		t.Fatal("comparison mutated/ran inference", err, inference.Load())
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("comparison created catalog", err)
	}
}
