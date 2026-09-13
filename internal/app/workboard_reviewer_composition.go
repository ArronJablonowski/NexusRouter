package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

// configuredWorkboardReviewerProvider defers construction until the durable
// auxiliary-review admission has been committed by EvaluationService. Every
// review gets a freshly resolved credential and an owned provider lifetime.
type configuredWorkboardReviewerProvider struct {
	open func(context.Context) (providers.Provider, func(), error)
}

func (*configuredWorkboardReviewerProvider) Models(context.Context) ([]string, error) {
	return nil, ErrAdmission
}

func (p *configuredWorkboardReviewerProvider) Stream(ctx context.Context, request providers.Request,
	emit func(providers.Chunk) error,
) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
	}()
	if p == nil || p.open == nil || ctx == nil || ctx.Err() != nil || emit == nil {
		return ErrAdmission
	}
	provider, cleanup, err := p.open(ctx)
	if err != nil || nilTaskProvider(provider) {
		safeTaskProviderCleanup(cleanup)
		return ErrAdmission
	}
	defer safeTaskProviderCleanup(cleanup)
	return provider.Stream(ctx, request, emit)
}

// ConfiguredWorkboardCandidateReviewer resolves the exact scheduler reviewer
// alias without routing or discovery. The returned evaluator is inert until
// EvaluationService has durably admitted a bounded auxiliary review.
func (s *Service) ConfiguredWorkboardCandidateReviewer(ctx context.Context) (workboard.CandidateEvaluator, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || s.settings.Validate() != nil {
		return nil, ErrAdmission
	}
	scheduler := s.settings.Workboard.Scheduler
	judge := scheduler.AcceptanceJudge
	if !scheduler.Enabled || !judge.Enabled {
		return nil, ErrAdmission
	}
	reviewer, found := configuredApplicationModel(s.settings.Models, judge.ReviewerModel)
	if !found || reviewer.Locality != "local" || reviewer.EstimatedCost == nil || !applicationModelHasCapability(reviewer, "audit") {
		return nil, ErrAdmission
	}
	provider, found := configuredApplicationProvider(s.settings.Providers, reviewer.Provider)
	if !found || (provider.Kind != "ollama" && provider.Kind != "openai_compatible") {
		return nil, ErrAdmission
	}
	timeout, err := config.Duration(judge.Timeout)
	if err != nil {
		return nil, ErrAdmission
	}
	configID, err := settingsConfigID(s.settings)
	secrets, secretsErr := memorySecretsSafely(s.settings, s.secret)
	if err != nil || secretsErr != nil || !selectionValueClean([]string{reviewer.ID, reviewer.Model, provider.ID, configID}, secrets) {
		return nil, ErrAdmission
	}
	if _, err = configuredProviderKey(s.secret, provider); err != nil {
		return nil, ErrAdmission
	}

	deferred := &configuredWorkboardReviewerProvider{open: func(openCtx context.Context) (providers.Provider, func(), error) {
		key, keyErr := configuredProviderKey(s.secret, provider)
		if keyErr != nil {
			return nil, nil, ErrAdmission
		}
		return s.openAuxiliaryProvider(openCtx, provider, reviewer, "local_only", key)
	}}
	projection := workboardCandidateReviewerConfig{
		ReviewerID: reviewer.ID, ModelID: reviewer.Model, ProviderID: provider.ID, ConfigID: configID, Local: true,
		ContextTokens: reviewer.ContextTokens, MaxInputTokens: judge.MaxInputTokens, MaxOutputTokens: judge.MaxOutputTokens,
		Timeout: timeout, EstimatedCost: *reviewer.EstimatedCost, MaxCost: judge.MaxCost, StructuredOutput: true,
	}
	result := newWorkboardCandidateReviewer(projection, deferred, s.contextEstimator, func() []string {
		return memorySecrets(s.settings, s.secret)
	})
	if result.invalidConfiguration() {
		return nil, ErrAdmission
	}
	return result, nil
}

func configuredApplicationModel(models []config.Model, id string) (config.Model, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return config.Model{}, false
}

func configuredApplicationProvider(providers []config.Provider, id string) (config.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return config.Provider{}, false
}

func applicationModelHasCapability(model config.Model, want string) bool {
	for _, capability := range model.Capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func configuredProviderKey(secret func(string) string, provider config.Provider) (key string, err error) {
	defer func() {
		if recover() != nil {
			key, err = "", ErrAdmission
		}
	}()
	if provider.APIKeyEnv == "" {
		return "", nil
	}
	if secret == nil {
		return "", ErrAdmission
	}
	value := secret(provider.APIKeyEnv)
	if value == "" {
		return "", ErrAdmission
	}
	return value, nil
}

func memorySecretsSafely(settings config.Settings, secret func(string) string) (values []string, err error) {
	defer func() {
		if recover() != nil {
			values, err = nil, ErrAdmission
		}
	}()
	return memorySecrets(settings, secret), nil
}
