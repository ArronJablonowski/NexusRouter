package cli

import (
	"errors"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

func workboardSchedulerReady(enabled bool, scheduler *app.WorkboardScheduleSupervisor) bool {
	return !enabled || scheduler != nil && scheduler.Health() == (app.WorkboardScheduleHealth{Status: "healthy", Code: "supervisor_ok"})
}

// withWorkboardSchedulerHealth converts the app-owned status only at the daemon
// composition boundary. Presence means scheduling is enabled, so shared health
// readiness requires this distinct supervisor to be healthy.
func withWorkboardSchedulerHealth(report health.Report, state app.WorkboardScheduleHealth) (health.Report, error) {
	if report.Validate() != nil || state.Validate() != nil {
		return health.Report{}, errors.New("workboard scheduler health unavailable")
	}
	check := health.Check{Component: "workboard_scheduler", Status: state.Status, Code: state.Code}
	if check.Validate() != nil {
		return health.Report{}, errors.New("workboard scheduler health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), check)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("workboard scheduler health unavailable")
	}
	return report, nil
}
