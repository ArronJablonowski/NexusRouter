package cli

import (
	"errors"

	"github.com/ArronJablonowski/NexusRouter/health"
)

func metricsExportDegraded(check health.Check) bool {
	return check.Component != "metrics_export" || check.Validate() != nil || check.Status == "degraded" || check.Status == "unavailable"
}

func withMetricsExportHealth(report health.Report, check health.Check) (health.Report, error) {
	if report.Validate() != nil || check.Component != "metrics_export" || check.Validate() != nil {
		return health.Report{}, errors.New("metrics export health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), check)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("metrics export health unavailable")
	}
	return report, nil
}
