package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestConfiguredEvaluatorAdmitsRedactsAndReleases(t *testing.T) {
	service, _ := autoFixture(t)
	var calls atomic.Int32
	coordinator := newReservationCoordinatorFixture()
	installReservationFixture(t, service, coordinator)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string
			Tools    []any
			Messages []struct{ Content string }
			Options  struct {
				NumPredict int64 `json:"num_predict"`
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "z" || len(body.Tools) != 0 || body.Options.NumPredict != 4096 {
			t.Error("invalid bounded review request", body)
		}
		for _, message := range body.Messages {
			if strings.Contains(message.Content, "private-token") {
				t.Error("secret escaped in review input")
			}
		}
		coordinator.mu.Lock()
		active := len(coordinator.active)
		coordinator.mu.Unlock()
		if active != 1 {
			t.Error("provider called without reservation", active)
		}
		a := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: evaluation.ReviewRubricVersion, Domain: "writing", Verdict: "reject", Confidence: .8, Findings: []evaluation.AuditFinding{{Summary: "private-token missing content", EvidenceRefs: []string{"candidate"}}}}
		data, _ := json.Marshal(a)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", string(data))
	}))
	defer server.Close()
	service.settings.Providers[0].Endpoint = server.URL
	service.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "private-token"
		}
		return ""
	}
	evaluator, local, err := service.ConfiguredEvaluator("z", 0, true)
	if err != nil || !local || calls.Load() != 0 {
		t.Fatal("construction performed inference", err)
	}
	request := evaluation.EvaluatorRequest{Version: 1, Domain: "writing", Requirements: "private-token request", Candidate: "private-token answer"}
	result, err := evaluation.InvokeEvaluator(context.Background(), evaluator, request, time.Minute)
	if err != nil || result.Audit.Verdict != "reject" || strings.Contains(result.Audit.Findings[0].Summary, "private-token") || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.active) != 0 || coordinator.releases != 1 {
		t.Fatal("reservation not released", coordinator.releases)
	}
}

func TestConfiguredEvaluatorAdmissionNeverReachesProvider(t *testing.T) {
	for _, mode := range []string{"disabled", "cost", "private_cloud", "missing_secret", "capacity", "config_drift", "context"} {
		t.Run(mode, func(t *testing.T) {
			service, _ := autoFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
			defer server.Close()
			service.settings.Providers[0].Endpoint = server.URL
			switch mode {
			case "disabled":
				service.settings.Evaluation.Judge = false
			case "cost":
				value := 1.0
				service.settings.Models[1].EstimatedCost = &value
			case "private_cloud":
				service.settings.Mode = "hybrid"
				service.settings.Models[1].Locality = "cloud"
			case "missing_secret":
				service.settings.Providers[0].APIKeyEnv = "MISSING_FIXTURE_KEY"
			case "capacity":
				service.profile = func(context.Context) (resources.Snapshot, error) {
					return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1}, nil
				}
			}
			evaluator, _, err := service.ConfiguredEvaluator("z", 0, true)
			if err == nil {
				if mode == "config_drift" {
					service.settings.Models[1].Model = "changed"
				}
				candidate := "answer"
				if mode == "context" {
					candidate = strings.Repeat("oversized ", 10000)
				}
				_, err = evaluation.InvokeEvaluator(context.Background(), evaluator, evaluation.EvaluatorRequest{Version: 1, Domain: "writing", Requirements: "request", Candidate: candidate}, time.Minute)
			}
			if err == nil || calls.Load() != 0 {
				t.Fatal("denied evaluator reached provider", mode, err, calls.Load())
			}
		})
	}
}
