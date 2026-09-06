package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// ConfiguredLearningPlan resolves named trusted host code without starting a
// supervisor, opening storage, or running a validator/model. Configuration does
// not provide executable validators; the host must supply the registry.
type ConfiguredLearningPlan struct {
	service            *Service
	disabled           bool
	settingsDigest     [32]byte
	validatorID        string
	validator          skills.Validator
	regressionName     string
	regressionInterval time.Duration
}

func PrepareConfiguredLearning(s *Service, registry *skills.ValidatorRegistry) (plan *ConfiguredLearningPlan, err error) {
	defer func() {
		if recover() != nil {
			plan = nil
			err = ErrLearningAttention
		}
	}()
	if s == nil {
		return nil, ErrLearningAttention
	}
	p := &ConfiguredLearningPlan{service: s, disabled: !s.settings.Skills.Learning.Enabled}
	if p.disabled {
		return p, nil
	}
	if s.settings.Validate() != nil || !s.settings.Skills.GenerationBudget.Enabled {
		return nil, ErrLearningAttention
	}
	learning := s.settings.Skills.Learning
	p.validatorID = learning.ValidatorID
	p.regressionName = learning.RegressionName
	if p.validatorID != "" {
		if registry == nil {
			return nil, ErrLearningAttention
		}
		p.validator, err = registry.Resolve(p.validatorID)
		if err != nil {
			return nil, ErrLearningAttention
		}
	}
	if p.regressionName != "" {
		p.regressionInterval, err = config.Duration(learning.RegressionInterval)
		if err != nil {
			return nil, ErrLearningAttention
		}
	}
	if err = p.policyReady(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(s.settings)
	if err != nil {
		return nil, ErrLearningAttention
	}
	p.settingsDigest = sha256.Sum256(body)
	return p, nil
}

func (p *ConfiguredLearningPlan) policyReady() error {
	s := p.service
	if s == nil || s.skillStore != nil || s.settings.Validate() != nil || !s.settings.Skills.Learning.Enabled || !s.settings.Skills.GenerationBudget.Enabled {
		return ErrLearningAttention
	}
	interval, err := config.Duration(s.settings.Skills.Learning.Interval)
	if err != nil || interval < time.Second || interval > 24*time.Hour {
		return ErrLearningAttention
	}
	var validation *learningValidation
	if p.validatorID != "" {
		validation, err = s.learningValidator(p.validatorID, p.validator)
		if err != nil {
			return ErrLearningAttention
		}
	}
	if _, err = s.learningPolicyForValidation(validation); err != nil {
		return ErrLearningAttention
	}
	if p.regressionName != "" {
		if p.validatorID == "" || !s.skillRegressionConfigured(p.validator) {
			return ErrLearningAttention
		}
		if _, err = s.regressionMonitorPolicy(p.regressionName, p.validatorID, p.regressionInterval); err != nil {
			return ErrLearningAttention
		}
	}
	return nil
}

// Start owns both supervisors under one cancellation tree. Configured regression
// requires an existing compatible catalog before either supervisor starts; it
// never creates a catalog merely to avoid an initial regression-monitor error.
func (p *ConfiguredLearningPlan) Start(ctx context.Context) (running *ConfiguredLearning, err error) {
	var owned *ConfiguredLearning
	defer func() {
		if recover() != nil {
			if owned != nil {
				_ = owned.Close()
			}
			running = nil
			err = ErrLearningAttention
		}
	}()
	if p == nil || p.service == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrLearningAttention
	}
	if p.disabled {
		return &ConfiguredLearning{disabled: true}, nil
	}
	body, err := json.Marshal(p.service.settings)
	if err != nil || sha256.Sum256(body) != p.settingsDigest || p.policyReady() != nil {
		return nil, ErrLearningAttention
	}
	preflight, finishPreflight := context.WithTimeout(ctx, 10*time.Second)
	defer finishPreflight()
	preflightErr := p.persistedReady(preflight)
	finishPreflight()
	if preflightErr != nil {
		return nil, ErrLearningAttention
	}
	if ctx.Err() != nil {
		return nil, ErrLearningAttention
	}
	ctx, cancel := context.WithCancel(ctx)
	owned = &ConfiguredLearning{cancel: cancel}
	if p.regressionName != "" {
		owned.regression, err = StartDurableSkillRegression(ctx, p.service, p.regressionName, p.validatorID, p.regressionInterval, p.validator)
		if err != nil {
			cancel()
			return nil, ErrLearningAttention
		}
	}
	if p.validatorID == "" {
		owned.learning, err = StartLearning(ctx, p.service)
	} else {
		owned.learning, err = StartLearningWithValidation(ctx, p.service, p.validatorID, p.validator)
	}
	if err != nil {
		cancel()
		_ = owned.Close()
		return nil, ErrLearningAttention
	}
	return owned, nil
}

// Existing durable policy mismatches fail before either supervisor can act.
// This is preflight, not an atomic lock spanning SQLite and the skill catalog;
// their own intent/revision fences remain authoritative during each tick.
func (p *ConfiguredLearningPlan) persistedReady(ctx context.Context) error {
	s := p.service
	scope := s.settings.Skills.Scope
	l := s.settings.Skills.Learning
	var validation *learningValidation
	if p.validatorID != "" {
		validation = &learningValidation{id: p.validatorID, validator: p.validator}
	}
	policy, err := s.learningPolicyForValidation(validation)
	if err != nil {
		return ErrLearningAttention
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return ErrLearningAttention
	}
	defer db.Close()
	state, err := db.LearningState(ctx, scope, l.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ErrLearningAttention
	}
	if err == nil && (state.Validate() != nil || state.Scope != scope || state.Name != l.Name || state.Domain != l.Domain || state.PolicyDigest != policy || !selectionValueClean(state, memorySecrets(s.settings, s.secret))) {
		return ErrLearningAttention
	}
	if p.regressionName == "" {
		return nil
	}
	regressionPolicy, err := s.regressionMonitorPolicy(p.regressionName, p.validatorID, p.regressionInterval)
	if err != nil {
		return ErrLearningAttention
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{scope})
	if err != nil {
		return ErrLearningAttention
	}
	defer store.Close()
	monitor, err := store.RegressionMonitorState(ctx, scope, p.regressionName)
	if err != nil && !errors.Is(err, skills.ErrNotFound) {
		return ErrLearningAttention
	}
	if err == nil && (monitor.Validate() != nil || monitor.Scope != scope || monitor.Name != p.regressionName || monitor.ValidatorID != p.validatorID || monitor.PolicyDigest != regressionPolicy || monitor.Interval != p.regressionInterval || !selectionValueClean(monitor, memorySecrets(s.settings, s.secret))) {
		return ErrLearningAttention
	}
	return ctx.Err()
}

// ConfiguredLearning only owns supervisors it started. Close cancels all work
// before joining either cooperative callback and is safe for concurrent callers.
type ConfiguredLearning struct {
	once       sync.Once
	cancel     context.CancelFunc
	learning   *Learner
	regression *SkillRegressionMonitor
	disabled   bool
	err        error
}

func (c *ConfiguredLearning) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		var a, b error
		if c.learning != nil {
			a = c.learning.Close()
		}
		if c.regression != nil {
			b = c.regression.Close()
		}
		c.err = errors.Join(a, b)
	})
	return c.err
}
func (c *ConfiguredLearning) Health() []health.Check {
	if c == nil {
		return []health.Check{{Component: "learning", Status: "unknown", Code: "supervisor_starting"}}
	}
	if c.disabled {
		return []health.Check{{Component: "learning", Status: "disabled", Code: "disabled_by_policy"}}
	}
	out := []health.Check{}
	if c.learning != nil {
		out = append(out, c.learning.Health())
	}
	if c.regression != nil {
		out = append(out, c.regression.Health())
	}
	return out
}
