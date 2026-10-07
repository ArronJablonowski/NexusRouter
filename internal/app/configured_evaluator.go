package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// ConfiguredEvaluator constructs an inert, policy-bound advisory evaluator for
// externally verified evidence. The caller must durably admit its invocation
// and authenticate that evidence; this does not import a remote task as local.
// Hosts must install their shared resource coordinator before invoking it.
func (s *Service) ConfiguredEvaluator(reviewerID string, maxCost float64, private bool) (evaluation.Evaluator, bool, error) {
	if s == nil || !s.settings.Evaluation.Judge || reviewerID == "" || !config.ModelUseAllowed(s.settings.Telemetry.Database, "local", reviewerID) || maxCost < 0 || maxCost > evaluation.MaxReviewCost || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
		return nil, false, ErrAdmission
	}
	var model config.Model
	for _, m := range s.settings.Models {
		if m.ID == reviewerID {
			model = m
			break
		}
	}
	local := model.Locality == "local"
	if model.ID == "" || model.EstimatedCost == nil || *model.EstimatedCost > maxCost || model.WorkingContextTokens() < 1 || (local && model.RAMBytes == 0) || (private && !local) || (s.settings.Mode == "local_only" && !local) || (s.settings.Mode == "cloud_only" && local) {
		return nil, false, ErrAdmission
	}
	var provider config.Provider
	for _, p := range s.settings.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	if provider.ID == "" {
		return nil, false, ErrAdmission
	}
	configID, err := settingsConfigID(s.settings)
	if err != nil {
		return nil, false, ErrAdmission
	}
	body, _ := json.Marshal(struct {
		Version, Config, Model string
		Cost                   float64
		Private                bool
	}{"configured-evaluator-v1", configID, model.ID, maxCost, private})
	sum := sha256.Sum256(body)
	descriptor := evaluation.EvaluatorDescriptor{Version: 1, ID: model.ID, Revision: hex.EncodeToString(sum[:]), RubricVersion: evaluation.ReviewRubricVersion}
	if descriptor.Validate() != nil {
		return nil, false, ErrAdmission
	}
	return &configuredEvaluator{service: s, model: model, provider: provider, configID: configID, descriptor: descriptor, maxCost: maxCost, private: private}, local, nil
}

type configuredEvaluator struct {
	service    *Service
	model      config.Model
	provider   config.Provider
	configID   string
	descriptor evaluation.EvaluatorDescriptor
	maxCost    float64
	private    bool
}

func (e *configuredEvaluator) Descriptor() evaluation.EvaluatorDescriptor { return e.descriptor }
func (e *configuredEvaluator) Evaluate(ctx context.Context, input evaluation.EvaluatorRequest) (evaluation.EvaluatorResponse, error) {
	bad := func() (evaluation.EvaluatorResponse, error) { return evaluation.EvaluatorResponse{}, ErrAdmission }
	if ctx == nil || ctx.Err() != nil || input.Validate() != nil || !config.ModelUseAllowed(e.service.settings.Telemetry.Database, "local", e.model.ID) {
		return bad()
	}
	current, err := settingsConfigID(e.service.settings)
	if err != nil || current != e.configID {
		return bad()
	}
	secrets := memorySecrets(e.service.settings, e.service.secret)
	labels := []string{input.Domain, e.descriptor.ID, e.descriptor.Revision, e.descriptor.RubricVersion, e.model.Model, e.provider.ID}
	for _, item := range input.Evidence {
		labels = append(labels, item.ID)
	}
	if !selectionValueClean(labels, secrets) {
		return bad()
	}
	key := ""
	if e.provider.APIKeyEnv != "" {
		if e.service.secret == nil {
			return bad()
		}
		key = e.service.secret(e.provider.APIKeyEnv)
		if key == "" {
			return bad()
		}
	}
	if key != "" {
		secrets = append(secrets, key)
	}
	if !selectionValueClean(labels, secrets) {
		return bad()
	}
	evidence := make([]evaluation.ReviewEvidence, len(input.Evidence))
	for i, item := range input.Evidence {
		evidence[i] = evaluation.ReviewEvidence{ID: item.ID, Content: redact(item.Content, secrets)}
	}
	requirements, candidate := redact(input.Requirements, secrets), redact(input.Candidate, secrets)
	release := func() error { return nil }
	if e.model.Locality == "local" {
		ctx, release, err = e.service.reserveAuxiliaryExecution(ctx, e.model)
		if err != nil {
			return bad()
		}
	}
	cleanup := auxiliaryCleanup(nil, release)
	defer func() { _ = cleanup() }()
	privacy := "cloud_allowed"
	if e.private {
		privacy = "local_only"
	}
	adapter, closeProvider, err := e.service.openAuxiliaryProvider(ctx, e.provider, e.model, privacy, key)
	if err != nil {
		return bad()
	}
	cleanup = auxiliaryCleanup(closeProvider, release)
	reviewer := evaluation.Reviewer{Provider: adapter, ContextEstimator: e.service.contextEstimator, Model: e.model.Model, EvaluatorID: e.model.ID, ContextTokens: e.model.WorkingContextTokens(), MaxOutputTokens: 4096, Timeout: time.Minute, EstimatedCost: *e.model.EstimatedCost, MaxCost: e.maxCost, StructuredOutput: e.provider.Kind == "codex_app_server"}
	result, err := reviewer.Review(ctx, evaluation.ReviewRequest{Domain: input.Domain, Requirements: requirements, Candidate: candidate, Evidence: evidence})
	cleanupErr := cleanup()
	if err != nil || cleanupErr != nil {
		return bad()
	}
	secrets = append(secrets, memorySecrets(e.service.settings, e.service.secret)...)
	for i := range result.Audit.Findings {
		result.Audit.Findings[i].Summary = redact(result.Audit.Findings[i].Summary, secrets)
	}
	if !selectionValueClean(result.Audit, secrets) {
		return bad()
	}
	return evaluation.EvaluatorResponse{Version: 1, Audit: result.Audit}, nil
}
