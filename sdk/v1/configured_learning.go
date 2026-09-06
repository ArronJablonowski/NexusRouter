package v1

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type SkillValidatorRegistry = skills.ValidatorRegistry

// NewSkillValidatorRegistry snapshots a bounded set of trusted, named Go
// validators. Identity must change when implementation semantics change.
func NewSkillValidatorRegistry(entries map[string]skills.Validator) (*SkillValidatorRegistry, error) {
	return skills.NewValidatorRegistry(entries)
}

// ConfiguredLearningSupervisor owns the configured learner and optional durable
// regression monitor. Client.Close is not required; the caller must Close this
// explicit supervisor and keep validators alive until Close finishes.
type ConfiguredLearningSupervisor struct{ supervisor *app.ConfiguredLearning }

func (s *ConfiguredLearningSupervisor) Close() error {
	if s == nil || s.supervisor == nil {
		return nil
	}
	return s.supervisor.Close()
}

func (s *ConfiguredLearningSupervisor) Health() []health.Check {
	if s == nil || s.supervisor == nil {
		return nil
	}
	return s.supervisor.Health()
}

// StartConfiguredLearning resolves configured validator identities before
// starting either controller. Validators are cooperative trusted host code,
// never loaded from configuration, model output, shell commands or URLs.
func (c *Client) StartConfiguredLearning(ctx context.Context, registry *SkillValidatorRegistry) (*ConfiguredLearningSupervisor, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	plan, err := app.PrepareConfiguredLearning(c.service, registry)
	if err != nil {
		return nil, ErrAdmission
	}
	supervisor, err := plan.Start(ctx)
	if err != nil {
		return nil, ErrAdmission
	}
	return &ConfiguredLearningSupervisor{supervisor: supervisor}, nil
}
