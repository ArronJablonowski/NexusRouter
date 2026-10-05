package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"time"
)

// BrowserSchedules reads immutable daemon configuration without starting work.
func (s *Service) BrowserSchedules(ctx context.Context) (webui.SchedulePage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return webui.SchedulePage{}, ErrAdmission
	}
	c := s.settings
	p := webui.SchedulePage{Version: 1, ObservedAt: time.Now().UTC(), Items: []webui.Schedule{}}
	add := func(id, description string, enabled bool, interval string) {
		if interval == "" {
			interval = "1m"
		}
		usage := "unknown"
		switch id {
		case "workboards", "skill-learning":
			usage = "possible"
		case "skill-regression", "outcome-supervision", "provider-health", "metrics-export", "trace-export", "harness-evidence":
			usage = "none"
		}
		p.Items = append(p.Items, webui.Schedule{AIUsage: usage, ID: id, Description: description, Enabled: enabled, Interval: interval})
	}
	add("workboards", "Process eligible workboard cards", c.Workboard.Scheduler.Enabled, c.Workboard.Scheduler.Interval)
	add("skill-learning", "Learn skills from recorded outcomes", c.Skills.Learning.Enabled, c.Skills.Learning.Interval)
	if c.Skills.Learning.RegressionName != "" {
		add("skill-regression", "Check recorded skill regressions", c.Skills.Learning.Enabled, c.Skills.Learning.RegressionInterval)
	}
	add("outcome-supervision", "Inspect recorded outcomes for rollback eligibility", c.Skills.OutcomeRollbackSupervisor.Enabled, c.Skills.OutcomeRollbackSupervisor.Interval)
	add("provider-health", "Record provider health observations", c.Telemetry.ProviderHealthHistory.Enabled, c.Telemetry.ProviderHealthHistory.Interval)
	if c.Telemetry.MetricsExport != nil {
		d, _ := c.Telemetry.MetricsExport.IntervalDuration()
		add("metrics-export", "Export recorded metrics", c.Telemetry.MetricsExport.Enabled, d.String())
	}
	if c.Telemetry.TraceExport != nil {
		d, _ := c.Telemetry.TraceExport.IntervalDuration()
		add("trace-export", "Export recorded traces", c.Telemetry.TraceExport.Enabled, d.String())
	}
	add("harness-evidence", "Reconcile native harness outcome evidence", s.harnessEvidence != nil, "1s")
	return p, p.Validate()
}
