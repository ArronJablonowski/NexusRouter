package cli

import (
	"errors"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
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

func configuredLearningReady(learner *app.ConfiguredLearning) bool {
	if learner == nil {
		return false
	}
	checks := learner.Health()
	if !validConfiguredLearningChecks(checks) {
		return false
	}
	for _, check := range checks {
		if check.Status != "healthy" && check.Status != "disabled" {
			return false
		}
	}
	return true
}

func validConfiguredLearningChecks(checks []health.Check) bool {
	if len(checks) < 1 || len(checks) > 2 || checks[0].Component != "learning" {
		return false
	}
	for i, check := range checks {
		if check.Validate() != nil || (i == 1 && check.Component != "skill_regression") {
			return false
		}
	}
	return true
}

func withConfiguredLearningHealth(report health.Report, checks []health.Check) (health.Report, error) {
	if report.Validate() != nil || !validConfiguredLearningChecks(checks) {
		return health.Report{}, errors.New("learning health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), checks...)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("learning health unavailable")
	}
	return report, nil
}
