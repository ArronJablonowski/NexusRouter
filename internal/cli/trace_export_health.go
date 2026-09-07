package cli

import (
	"errors"

	"github.com/ArronJablonowski/DarwinRouter/health"
)

func traceExportDegraded(check health.Check) bool {
	return check.Component != "trace_export" || check.Validate() != nil || check.Status == "degraded" || check.Status == "unavailable"
}

func withTraceExportHealth(report health.Report, check health.Check) (health.Report, error) {
	if report.Validate() != nil || check.Component != "trace_export" || check.Validate() != nil {
		return health.Report{}, errors.New("trace export health unavailable")
	}
	report.Checks = append(append([]health.Check(nil), report.Checks...), check)
	report.Status, report.Ready = health.Outcome(report.Checks)
	if report.Validate() != nil {
		return health.Report{}, errors.New("trace export health unavailable")
	}
	return report, nil
}
