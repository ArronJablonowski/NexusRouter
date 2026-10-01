package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestRemoteConfiguredEvaluatorProviderIsNotReplayed(t *testing.T) {
	f, routes, key, task, v, _, backend := reviewFixture(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		audit := evaluation.Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: evaluation.ReviewRubricVersion, Domain: task.Domain, Verdict: "accept", Confidence: .7, Findings: []evaluation.AuditFinding{{Summary: "fixture candidate meets request", EvidenceRefs: []string{"candidate", "requirements"}}}}
		body, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", string(body))
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "local.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "reviewer", Model: "judge", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, RAMBytes: 100, EstimatedCost: &zero}}
	service, err := app.NewServiceWithProfiler(cfg, nil, fixtureProfiler{})
	if err != nil {
		t.Fatal(err)
	}
	closeCoordinator, err := app.InstallHostResourceCoordinator(context.Background(), service, "remote-eval-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer closeCoordinator()
	evaluator, local, err := service.ConfiguredEvaluator("reviewer", 0, task.Private)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	policy := RemoteEvaluator{Evaluator: evaluator, Local: local, Timeout: time.Minute}
	for i := 0; i < 2; i++ {
		result, err := f.client.EvaluateRecordedOutcome(context.Background(), routes, root, key, task, policy)
		if err != nil || !result.ReviewApplied {
			t.Fatal(result, err)
		}
	}
	rank := remoteRank(t, root, v)
	if calls.Load() != 1 || backend.submits.Load() != 1 || rank.AdvisorySamples != 1 || rank.ConfirmedSamples != 0 {
		t.Fatal(calls.Load(), backend.submits.Load(), rank)
	}
}
