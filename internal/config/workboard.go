package config

import (
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const maxWorkboardJudgeCost = float64(workboard.MaxWorkCostMicros) / 1_000_000

func (s Settings) validateWorkboardDecomposition() error {
	limits := s.Workboard.Decomposition
	if limits.Version != 1 || limits.MaxDepth < 1 || limits.MaxDepth > workboard.MaxGraphDepth ||
		limits.MaxChildrenPerParent < 1 || limits.MaxChildrenPerParent > workboard.MaxChildrenPerParent {
		return errors.New("invalid workboard decomposition limits")
	}
	return nil
}

// validateWorkboardSchedulerModels keeps unattended Workboard execution
// fail-closed. The scheduler is not allowed to select either participant via
// adaptive routing: both aliases resolve to one configured provider/model
// identity before the daemon can start.
func (s Settings) validateWorkboardSchedulerModels() error {
	scheduler := s.Workboard.Scheduler
	judge := scheduler.AcceptanceJudge
	timeout, timeoutErr := Duration(judge.Timeout)
	if timeoutErr != nil || timeout < 100*time.Millisecond || timeout > 5*time.Minute {
		return errors.New("invalid workboard acceptance judge timeout")
	}
	if !finite(judge.MaxCost) || judge.MaxCost < 0 || judge.MaxCost > maxWorkboardJudgeCost {
		return errors.New("invalid workboard acceptance judge cost ceiling")
	}
	if judge.MaxOutputTokens < 0 || judge.MaxOutputTokens > providers.MaxOutputTokens {
		return errors.New("invalid workboard acceptance judge output-token ceiling")
	}
	if judge.MaxInputTokens < 0 || judge.MaxInputTokens > workboard.MaxWorkTokens ||
		judge.MaxOutputTokens > workboard.MaxWorkTokens-judge.MaxInputTokens {
		return errors.New("invalid workboard acceptance judge total-token ceiling")
	}
	if judge.Enabled && !scheduler.Enabled {
		return errors.New("workboard acceptance judge requires enabled scheduler")
	}
	if judge.Enabled && !s.Evaluation.Judge {
		return errors.New("workboard acceptance judge requires global LLM judge gate")
	}

	worker, workerFound := configuredModel(s.Models, scheduler.WorkerModel)
	reviewer, reviewerFound := configuredModel(s.Models, judge.ReviewerModel)
	if scheduler.WorkerModel != "" && (!identifier.MatchString(scheduler.WorkerModel) || !workerFound) {
		return errors.New("unknown workboard scheduler worker model")
	}
	if judge.ReviewerModel != "" && (!identifier.MatchString(judge.ReviewerModel) || !reviewerFound) {
		return errors.New("unknown workboard acceptance reviewer model")
	}
	if !scheduler.Enabled {
		return nil
	}
	if !workerFound || !judge.Enabled || !reviewerFound || judge.MaxCost <= 0 || judge.MaxInputTokens <= 0 || judge.MaxOutputTokens <= 0 {
		return errors.New("enabled workboard scheduler requires bounded worker and acceptance reviewer")
	}
	if s.Workers.Max > workboard.MaxExecutionWIPLimit {
		return errors.New("workboard scheduler worker limit exceeds execution admission")
	}
	if !modelHasCapability(worker, "chat") || !modelAvailableInMode(worker, s.Mode) || worker.ContextTokens < 1 || worker.EstimatedCost == nil ||
		!finite(*worker.EstimatedCost) || *worker.EstimatedCost < 0 || (worker.Locality != "local" && *worker.EstimatedCost == 0) {
		return errors.New("workboard scheduler worker unavailable within configured limits")
	}
	if !modelHasCapability(reviewer, "audit") || reviewer.Locality != "local" || !modelAvailableInMode(reviewer, s.Mode) || reviewer.ContextTokens < 1 || reviewer.EstimatedCost == nil ||
		!finite(*reviewer.EstimatedCost) || *reviewer.EstimatedCost < 0 || *reviewer.EstimatedCost > maxWorkboardJudgeCost || *reviewer.EstimatedCost > judge.MaxCost {
		return errors.New("workboard acceptance reviewer unavailable within configured limits")
	}
	if judge.MaxInputTokens+judge.MaxOutputTokens > int64(reviewer.ContextTokens) {
		return errors.New("workboard acceptance reviewer token reservation exceeds model context")
	}
	workerProviderKind, providerKind := "", ""
	for _, provider := range s.Providers {
		if provider.ID == worker.Provider {
			workerProviderKind = provider.Kind
		}
		if provider.ID == reviewer.Provider {
			providerKind = provider.Kind
		}
	}
	// Unattended Workboard execution always carries a positive output-token
	// ceiling. Codex app-server currently cannot enforce that provider-side, so
	// accepting it here would defer a deterministic configuration failure until
	// after the scheduler had selected a card.
	if workerProviderKind != "ollama" && workerProviderKind != "openai_compatible" {
		return errors.New("workboard scheduler worker cannot enforce output-token ceiling")
	}
	if providerKind != "ollama" && providerKind != "openai_compatible" {
		return errors.New("workboard acceptance reviewer cannot enforce output-token ceiling")
	}
	if worker.Provider == reviewer.Provider && worker.Model == reviewer.Model {
		return errors.New("workboard worker and acceptance reviewer must be independent models")
	}
	return nil
}

func configuredModel(models []Model, alias string) (Model, bool) {
	for _, model := range models {
		if model.ID == alias {
			return model, true
		}
	}
	return Model{}, false
}

func modelHasCapability(model Model, capability string) bool {
	for _, configured := range model.Capabilities {
		if configured == capability {
			return true
		}
	}
	return false
}

func modelAvailableInMode(model Model, mode string) bool {
	return mode == "hybrid" || (mode == "local_only" && model.Locality == "local") || (mode == "cloud_only" && model.Locality == "cloud")
}
