package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func configuredWorkboardReviewerSettings() config.Settings {
	settings := config.Defaults()
	workerCost, reviewerCost := .5, .1
	settings.Providers = []config.Provider{
		{ID: "cloud", Kind: "openai_compatible", Endpoint: "https://example.invalid/v1"},
		{ID: "local", Kind: "openai_compatible", Endpoint: "http://127.0.0.1:11434/v1", APIKeyEnv: "LOCAL_REVIEW_KEY"},
	}
	settings.Models = []config.Model{
		{ID: "worker", Provider: "cloud", Model: "worker-native", Locality: "cloud", ContextTokens: 8192, EstimatedCost: &workerCost, Capabilities: []string{"chat"}},
		{ID: "reviewer", Provider: "local", Model: "reviewer-native", Locality: "local", ContextTokens: 100_000, EstimatedCost: &reviewerCost, Capabilities: []string{"audit"}},
	}
	settings.Workboard.Scheduler.Enabled = true
	settings.Workboard.Scheduler.WorkerModel = "worker"
	settings.Workboard.Scheduler.AcceptanceJudge = config.WorkboardAcceptanceJudge{
		Enabled: true, ReviewerModel: "reviewer", MaxCost: .25, MaxInputTokens: 90_000, MaxOutputTokens: 4096, Timeout: "30s",
	}
	return settings
}

func TestConfiguredWorkboardReviewerDefersAuxiliaryProviderAndRefreshesCredential(t *testing.T) {
	settings := configuredWorkboardReviewerSettings()
	var mu sync.Mutex
	key := "first-local-review-key"
	connections := []providers.Connection{}
	provider := &workboardReviewProvider{response: workboardAuditResponse("abstain", []any{})}
	service, err := NewService(settings, func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		if name == "LOCAL_REVIEW_KEY" {
			return key
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	service.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) { return 512, nil })
	service.providerFactory = applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
		mu.Lock()
		connections = append(connections, connection)
		mu.Unlock()
		return provider, nil
	})

	evaluator, err := service.ConfiguredWorkboardCandidateReviewer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(connections) != 0 {
		t.Fatal("review provider constructed before durable evaluation admission")
	}
	key = "rotated-local-review-key"
	mu.Unlock()
	frozen := workboardReviewFrozen(t)
	reservation, reservationErr := evaluator.(*workboardCandidateReviewer).AuxiliaryReviewReservation(frozen)
	if reservationErr != nil {
		t.Fatalf("configured reservation failed: %+v %v", reservation, reservationErr)
	}
	result, err := evaluator.(interface {
		EvaluateBudgetedCandidate(context.Context, workboard.CandidateEvaluationRequest) (workboard.BudgetedCandidateEvaluation, error)
	}).EvaluateBudgetedCandidate(context.Background(), frozen)
	if err != nil || result.Audit.Validate() != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("configured review failed: %+v %v calls=%d connections=%+v request=%+v", result, err, provider.calls.Load(), connections, provider.request)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(connections) != 1 || connections[0].Purpose != providers.PurposeAuxiliary ||
		connections[0].ID != "local" || connections[0].APIKey != "rotated-local-review-key" || connections[0].Transport == nil {
		t.Fatalf("wrong auxiliary construction: %+v", connections)
	}
	if provider.calls.Load() != 1 || provider.request.Model != "reviewer-native" || len(provider.request.Tools) != 0 ||
		provider.request.MaxOutputTokens != 4096 || len(provider.request.JSONSchema) == 0 {
		t.Fatalf("unbounded configured review request: calls=%d request=%+v", provider.calls.Load(), provider.request)
	}
}

func TestConfiguredWorkboardReviewerRejectsMissingOrPanickingCredentialBeforeFactory(t *testing.T) {
	for _, test := range []struct {
		name   string
		secret func(string) string
	}{
		{name: "missing"},
		{name: "empty", secret: func(string) string { return "" }},
		{name: "panic", secret: func(string) string { panic("private") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(configuredWorkboardReviewerSettings(), test.secret)
			if err != nil {
				t.Fatal(err)
			}
			built := false
			service.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				built = true
				return nil, errors.New("must not build")
			})
			if _, err = service.ConfiguredWorkboardCandidateReviewer(context.Background()); !errors.Is(err, ErrAdmission) {
				t.Fatalf("credential failure accepted: %v", err)
			}
			if built {
				t.Fatal("provider factory ran after credential admission failed")
			}
		})
	}
}
