package cli

import (
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func configuredOutcomeSupervisionReady(supervisor *app.ConfiguredOutcomeSupervision) bool {
	if supervisor == nil {
		return false
	}
	check := supervisor.Health()
	return check.Component == "outcome_supervision" && check.Validate() == nil && (check.Status == "healthy" || check.Status == "disabled")
}

func withConfiguredOutcomeSupervisionHealth(report health.Report, check health.Check) (health.Report, error) {
	if report.Validate() != nil || check.Component != "outcome_supervision" || check.Validate() != nil {
		return health.Report{}, errors.New("outcome supervision health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), check)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("outcome supervision health unavailable")
	}
	return report, nil
}
