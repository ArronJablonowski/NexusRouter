package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// ConfiguredOutcomeSupervisionPlan freezes policy without opening durable
// stores or starting background work. Start performs read-only preflight before
// the first inspection cycle.
type ConfiguredOutcomeSupervisionPlan struct {
	service        *Service
	disabled       bool
	settingsDigest [32]byte
}

func PrepareConfiguredOutcomeSupervision(service *Service) (*ConfiguredOutcomeSupervisionPlan, error) {
	if service == nil || service.settings.Validate() != nil {
		return nil, ErrAdmission
	}
	plan := &ConfiguredOutcomeSupervisionPlan{service: service, disabled: !service.settings.Skills.OutcomeRollbackSupervisor.Enabled}
	body, err := json.Marshal(service.settings)
	if err != nil {
		return nil, ErrAdmission
	}
	plan.settingsDigest = sha256.Sum256(body)
	if !plan.disabled && !service.outcomeSupervisionConfigured() {
		return nil, ErrAdmission
	}
	return plan, nil
}

func (p *ConfiguredOutcomeSupervisionPlan) Start(ctx context.Context) (*ConfiguredOutcomeSupervision, error) {
	if p == nil || p.service == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	body, err := json.Marshal(p.service.settings)
	if err != nil || sha256.Sum256(body) != p.settingsDigest {
		return nil, ErrAdmission
	}
	if p.disabled {
		return &ConfiguredOutcomeSupervision{disabled: true}, nil
	}
	if !p.service.outcomeSupervisionConfigured() {
		return nil, ErrAdmission
	}
	preflight, cancel := context.WithTimeout(ctx, outcomeSupervisionPreflightTimeout)
	defer cancel()
	catalog, err := skills.OpenReadOnly(p.service.settings.Skills.Root, []string{p.service.settings.Skills.Scope})
	if err != nil {
		return nil, ErrAdmission
	}
	interval, intervalErr := configuredOutcomeSupervisionInterval(p.service)
	policyID, policyErr := p.service.outcomeSupervisionPolicyDigest(interval)
	state, stateErr := catalog.OutcomeSupervisionState(preflight, p.service.settings.Skills.Scope, configuredOutcomeSupervisorName)
	if stateErr == nil && (state.Validate() != nil || state.PolicyDigest != policyID || state.Interval != interval) {
		stateErr = ErrAdmission
	}
	if err = catalog.Close(); err != nil || intervalErr != nil || policyErr != nil || stateErr != nil && !errors.Is(stateErr, skills.ErrNotFound) {
		return nil, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(preflight, p.service.settings.Telemetry.Database)
	if err != nil {
		return nil, ErrAdmission
	}
	if err = db.Close(); err != nil || preflight.Err() != nil {
		return nil, ErrAdmission
	}
	monitor, err := StartOutcomeSupervision(ctx, p.service)
	if err != nil {
		return nil, ErrAdmission
	}
	return &ConfiguredOutcomeSupervision{monitor: monitor}, nil
}

const outcomeSupervisionPreflightTimeout = 10 * time.Second

// ConfiguredOutcomeSupervision owns only the monitor it started.
type ConfiguredOutcomeSupervision struct {
	once     sync.Once
	monitor  *OutcomeSupervisionMonitor
	disabled bool
	err      error
}

func (s *ConfiguredOutcomeSupervision) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.monitor != nil {
			s.err = s.monitor.Close()
		}
	})
	return s.err
}

func (s *ConfiguredOutcomeSupervision) Health() health.Check {
	if s == nil {
		return health.Check{Component: "outcome_supervision", Status: "unknown", Code: "supervisor_starting"}
	}
	if s.disabled {
		return health.Check{Component: "outcome_supervision", Status: "disabled", Code: "disabled_by_policy"}
	}
	if s.monitor == nil {
		return health.Check{Component: "outcome_supervision", Status: "unknown", Code: "supervisor_starting"}
	}
	return s.monitor.Health()
}
