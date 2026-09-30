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
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillTaskOutcomeHTTPStoredAttributionAndCurrentFeedback(t *testing.T) {
	ctx := context.Background()
	var inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { inference.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Skills.Scope = "project"
	cfg.Skills.Root = filepath.Join(t.TempDir(), "absent-catalog")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "model", Model: "fixture-model", Provider: "local", Locality: "local", ContextTokens: 4096, RAMBytes: 1, Capabilities: []string{"creative"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	use := taskSkillOutcomeFixture().SkillContext
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: "task-" + string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Unix(100, 0).UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		switch kind {
		case runtime.TaskStarted:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "local", Domain: "creative", Profile: "default", Privacy: "local_only", SkillContext: use, Messages: []providers.Message{{Role: "user", Content: "PRIVATE_SKILL_BODY PRIVATE_USER_PROMPT"}}}
		case runtime.TurnStarted:
			e.Data = runtime.Data{ModelID: "model", ProviderID: "local"}
		case runtime.TurnCompleted:
			e.Data = runtime.Data{Text: "PRIVATE_MODEL_OUTPUT", FinishReason: "stop"}
		case runtime.EvaluationRecorded:
			passed := true
			e.Data = runtime.Data{ModelID: "model", ProviderID: "local", Domain: "creative", Profile: "default", Code: "deterministic.nonempty_text.v1", Accepted: &passed}
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	var rotated atomic.Bool
	svc, err := app.NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			if rotated.Load() {
				return "lookup"
			}
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.SkillTaskOutcome = svc.SkillTaskOutcome
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	call := func(want int) skills.TaskOutcome {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+"/v1/tasks/task/skill-outcome", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || response.StatusCode != want || len(body) > 65536 || strings.Contains(string(body), "PRIVATE_") || strings.Contains(string(body), token) {
			t.Fatal(response.StatusCode, string(body), err)
		}
		var out skills.TaskOutcome
		if want == 200 && json.Unmarshal(body, &out) != nil {
			t.Fatal("invalid report")
		}
		return out
	}
	before, err := db.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SkillTaskOutcome(ctx, "task"); err != nil {
		t.Fatal("stored fixture invalid", err)
	}
	if _, err := svc.SkillTaskOutcome(ctx, "task"); err != nil {
		t.Fatal("service fixture invalid", err)
	}
	unknown := call(200)
	if unknown.Quality != nil || len(unknown.OutputChecks) != 1 || !unknown.OutputChecks[0].Passed || !reflect.DeepEqual(unknown.SkillContext, use) {
		t.Fatal("mechanical check changed quality or attribution", unknown)
	}
	record := evaluation.Record{Version: 1, ID: "feedback-first", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "local", Domain: "creative", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user-first", Passed: true}}, ExecutionSucceeded: true, Time: time.Unix(101, 0).UTC()}
	if err := db.RecordEvaluation(ctx, record); err != nil {
		t.Fatal(err)
	}
	first := call(200)
	if first.Quality == nil || !first.Quality.Accepted || first.Quality.Source != evaluation.UserFeedback {
		t.Fatal(first)
	}
	next := record
	next.ID = "feedback-correction"
	next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "user-corrected", Passed: false}}
	if err := db.SupersedeEvaluation(ctx, record.ID, next); err != nil {
		t.Fatal(err)
	}
	last := call(200)
	if last.Quality == nil || last.Quality.Accepted || last.EvaluationID != next.ID || last.EvaluationDigest == first.EvaluationDigest {
		t.Fatal("stale quality projection", last)
	}
	rotated.Store(true)
	call(503)
	after, err := db.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || inference.Load() != 0 {
		t.Fatal("inspection changed journal or invoked provider", err, inference.Load())
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("inspection created or required absent skill catalog", err)
	}
}
