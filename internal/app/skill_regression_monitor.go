package app

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// SkillRegressionStep checks one currently active skill after the lexical name
// cursor. The cursor schedules inspection; it is not a completion receipt. A
// check failure still returns the observed name so other skills are not starved.
// Empty discovery wraps to the beginning. Restarting may repeat read-only checks;
// rollback authority always comes from the freshly inspected catalog revision.
func (s *Service) SkillRegressionStep(ctx context.Context, after string, validator skills.Validator) (next string, err error) {
	next = after
	defer func() {
		if recover() != nil {
			next, err = "", ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || !s.skillRegressionConfigured(validator) || (after != "" && !skillGenerationIdentifier.MatchString(after)) {
		return after, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]string{s.settings.Skills.Scope, after}, secrets) {
		return "", ErrAdmission
	}
	store, err := skills.OpenReadOnly(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return after, ErrAdmission
	}
	states, readErr := store.ActiveStates(ctx, s.settings.Skills.Scope, after, 1)
	closeErr := store.Close()
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean(after, secrets) {
		return "", ErrAdmission
	}
	if readErr != nil || closeErr != nil || ctx.Err() != nil || len(states) > 1 {
		return after, ErrAdmission
	}
	if len(states) == 0 {
		return "", nil
	}
	state := states[0]
	if state.Validate() != nil || state.Active == "" || state.Key.Scope != s.settings.Skills.Scope || state.Key.Name <= after || !selectionValueClean(state, secrets) {
		return after, ErrAdmission
	}
	next = state.Key.Name
	_, checkErr := s.RevalidateSkillVersion(ctx, state, validator)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if !selectionValueClean([]string{after, next}, secrets) {
		return "", ErrAdmission
	}
	if checkErr != nil {
		return next, ErrAdmission
	}
	return next, nil
}

func (s *Service) skillRegressionConfigured(validator skills.Validator) bool {
	return s != nil && s.skillActivationConfigured(skills.Key{Scope: s.settings.Skills.Scope, Name: "monitor"}) && s.settings.Skills.Rollback && validator != nil && !nilSkillActivationValidator(validator)
}

// SkillRegressionMonitor is an explicitly started trusted-host monitor. It
// serializes checks within this instance, never runs inference or task tools,
// and joins its cooperative validator on Close. Other instances may race safely
// through the existing activation-revision fence; validators must be read-only,
// concurrency-safe and cancellation-cooperative. It is not a sandbox.
type SkillRegressionMonitor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	err         error
	status      string
	code        string
	stepStarted time.Time
}

func StartSkillRegression(ctx context.Context, s *Service, interval time.Duration, validator skills.Validator) (*SkillRegressionMonitor, error) {
	if ctx == nil || ctx.Err() != nil || !s.skillRegressionConfigured(validator) || interval < time.Second || interval > 24*time.Hour {
		return nil, ErrAdmission
	}
	after := ""
	return startSkillRegressionMonitor(ctx, interval, func(ctx context.Context) error {
		next, err := s.SkillRegressionStep(ctx, after, validator)
		after = next
		return err
	})
}

// StartDurableSkillRegression retains the named cursor, due time and pending
// check across restarts. It does not enable daemon learning or invent a validator.
func StartDurableSkillRegression(ctx context.Context, s *Service, name, validatorID string, interval time.Duration, validator skills.Validator) (*SkillRegressionMonitor, error) {
	if ctx == nil || ctx.Err() != nil || !s.skillRegressionConfigured(validator) {
		return nil, ErrAdmission
	}
	if _, err := s.regressionMonitorPolicy(name, validatorID, interval); err != nil {
		return nil, err
	}
	return startSkillRegressionMonitor(ctx, interval, func(ctx context.Context) error {
		_, err := s.DurableSkillRegressionStep(ctx, name, validatorID, interval, validator)
		return err
	})
}

func startSkillRegressionMonitor(ctx context.Context, interval time.Duration, step func(context.Context) error) (*SkillRegressionMonitor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &SkillRegressionMonitor{cancel: cancel, done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	go func() {
		defer close(m.done)
		defer func() {
			m.mu.Lock()
			m.stepStarted = time.Time{}
			m.status, m.code = "unavailable", "supervisor_stopped"
			m.mu.Unlock()
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			m.mu.Lock()
			m.stepStarted = time.Now()
			m.mu.Unlock()
			err := step(ctx)
			if ctx.Err() != nil {
				return
			}
			m.mu.Lock()
			m.stepStarted = time.Time{}
			if err != nil {
				m.err = ErrAdmission
			}
			if m.err != nil {
				m.status, m.code = "degraded", "supervisor_error"
			} else {
				m.status, m.code = "healthy", "supervisor_ok"
			}
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return m, nil
}

func (m *SkillRegressionMonitor) Close() error {
	if m == nil || m.done == nil {
		return nil
	}
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *SkillRegressionMonitor) Health() health.Check {
	out := health.Check{Component: "skill_regression", Status: "unknown", Code: "supervisor_starting"}
	if m == nil || m.done == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out.Status, out.Code = m.status, m.code
	if !m.stepStarted.IsZero() && time.Since(m.stepStarted) > 10*time.Second {
		out.Status, out.Code = "degraded", "supervisor_stalled"
	}
	return out
}
