package v1

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

type SkillValidatorRegistry = skills.ValidatorRegistry

// ObservedToolsActivationValidatorID selects NexusRouter's protected,
// deterministic observed-tools provenance validator. Setting
// skills.learning.validator_id to this value is an explicit opt-in; callers do
// not register a callback for this identity.
const ObservedToolsActivationValidatorID = app.ObservedToolsProvenanceValidatorID

func reservedSkillValidatorID(id string) bool {
	return id == ObservedToolsActivationValidatorID
}

// NewSkillValidatorRegistry snapshots a bounded set of trusted, named Go
// validators. Identity must change when implementation semantics change.
func NewSkillValidatorRegistry(entries map[string]skills.Validator) (*SkillValidatorRegistry, error) {
	return skills.NewValidatorRegistry(entries)
}

// ConfiguredSkillValidatorRegistry snapshots host validators and adds the
// protected NexusRouter validators bound to this client's frozen settings.
// It performs no I/O and is safe to call before durable stores exist. Passing
// a protected identity in entries is rejected.
func (c *Client) ConfiguredSkillValidatorRegistry(entries map[string]skills.Validator) (*SkillValidatorRegistry, error) {
	if c == nil || c.service == nil {
		return nil, ErrAdmission
	}
	host, err := skills.NewValidatorRegistry(entries)
	if err != nil {
		return nil, ErrAdmission
	}
	registry, err := app.BuildConfiguredSkillValidatorRegistry(c.service, host)
	if err != nil {
		return nil, ErrAdmission
	}
	return registry, nil
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
	registry, err := app.BuildConfiguredSkillValidatorRegistry(c.service, registry)
	if err != nil {
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
