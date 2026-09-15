package app

import (
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// BuildConfiguredSkillValidatorRegistry merges product-owned validators into
// an immutable host registry. Call it only after NewService has frozen
// settings, and before configured-learning preflight or any durable mutation.
// It is pure: catalog and telemetry stores remain unopened until validation.
func BuildConfiguredSkillValidatorRegistry(s *Service, host *skills.ValidatorRegistry) (*skills.ValidatorRegistry, error) {
	if s == nil || s.settings.Validate() != nil {
		return nil, ErrLearningAttention
	}
	stock := ObservedToolsProvenanceValidator{
		Database:  s.settings.Telemetry.Database,
		LocalOnly: s.settings.Mode == "local_only" || s.settings.Skills.LocalOnly,
		root:      s.settings.Skills.Root,
		scope:     s.settings.Skills.Scope,
	}
	if host != nil {
		existing, resolveErr := host.Resolve(ObservedToolsProvenanceValidatorID)
		if resolveErr == nil {
			configured, ok := existing.(ObservedToolsProvenanceValidator)
			if !ok || configured.Publications != nil || configured.Database != stock.Database || configured.LocalOnly != stock.LocalOnly || configured.root != stock.root || configured.scope != stock.scope {
				return nil, ErrLearningAttention
			}
			return host, nil
		}
		if !errors.Is(resolveErr, skills.ErrNotFound) {
			return nil, ErrLearningAttention
		}
	}
	// Merely shipping a stock validator must not change custom-only registry
	// capacity or make activation implicit. Configuration selects the protected
	// identity before it is installed; every other identity remains host-owned.
	if s.settings.Skills.Learning.ValidatorID != ObservedToolsProvenanceValidatorID {
		return host, nil
	}
	registry, err := skills.WithProtectedValidator(host, ObservedToolsProvenanceValidatorID, stock)
	if err != nil {
		return nil, ErrLearningAttention
	}
	return registry, nil
}
