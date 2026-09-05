package cli

import (
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func learningReady(learner *app.Learner) bool {
	if learner == nil {
		return false
	}
	check := learner.Health()
	return check.Component == "learning" && check.Validate() == nil && (check.Status == "healthy" || check.Status == "disabled")
}

func withLearningHealth(report health.Report, check health.Check) (health.Report, error) {
	if report.Validate() != nil || check.Component != "learning" || check.Validate() != nil {
		return health.Report{}, errors.New("learning health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), check)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("learning health unavailable")
	}
	return report, nil
}
