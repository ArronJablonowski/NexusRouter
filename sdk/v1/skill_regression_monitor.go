package v1

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// SkillRegressionMonitor owns an explicitly started background monitor. Its
// caller must Close it. Validators are trusted, read-only, repeat-safe host
// callbacks and must cooperate with cancellation; they are not sandboxed.
type SkillRegressionMonitor struct{ monitor *app.SkillRegressionMonitor }

func (m *SkillRegressionMonitor) Close() error {
	if m == nil {
		return nil
	}
	return m.monitor.Close()
}

func (m *SkillRegressionMonitor) Health() health.Check {
	if m == nil {
		return (*app.SkillRegressionMonitor)(nil).Health()
	}
	return m.monitor.Health()
}

// SkillRegressionStep checks one active skill after the lexical cursor. A
// deterministic failure may roll back under configured rollback policy. The
// returned cursor can advance on an individual check error; preserve it to avoid
// starving later skills. An empty page returns an empty cursor for the next scan.
func (c *Client) SkillRegressionStep(ctx context.Context, after string, validator skills.Validator) (string, error) {
	if !c.valid(ctx) {
		return after, ErrAdmission
	}
	return c.service.SkillRegressionStep(ctx, after, validator)
}

// StartSkillRegression starts one sequential check immediately and then per
// interval. Scheduling is ephemeral: restart rediscovers active catalog state.
// Rollback policy is independent of new activation and learning enablement.
func (c *Client) StartSkillRegression(ctx context.Context, interval time.Duration, validator skills.Validator) (*SkillRegressionMonitor, error) {
	if !c.valid(ctx) {
		return nil, ErrAdmission
	}
	monitor, err := app.StartSkillRegression(ctx, c.service, interval, validator)
	if err != nil {
		return nil, err
	}
	return &SkillRegressionMonitor{monitor: monitor}, nil
}
