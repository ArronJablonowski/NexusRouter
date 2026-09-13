package health

import (
	"testing"
	"time"
)

func TestWorkboardSchedulerHealthParticipatesInReadinessWhenPresent(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "worker", Status: "healthy", Code: "available"}}
	for _, check := range []Check{
		{Component: "workboard_scheduler", Status: "healthy", Code: "supervisor_ok"},
		{Component: "workboard_scheduler", Status: "unknown", Code: "supervisor_starting"},
		{Component: "workboard_scheduler", Status: "degraded", Code: "supervisor_stalled"},
		{Component: "workboard_scheduler", Status: "degraded", Code: "supervisor_error"},
		{Component: "workboard_scheduler", Status: "unavailable", Code: "supervisor_stopping"},
		{Component: "workboard_scheduler", Status: "unavailable", Code: "supervisor_stopped"},
	} {
		report := Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: append(append([]Check(nil), base...), check)}
		report.Status, report.Ready = Outcome(report.Checks)
		if report.Validate() != nil || report.Ready != (check.Status == "healthy") {
			t.Fatalf("scheduler readiness mismatch for %+v: %+v", check, report)
		}
	}
	without := Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: base}
	without.Status, without.Ready = Outcome(without.Checks)
	if without.Validate() != nil || !without.Ready {
		t.Fatal("legacy report without optional scheduler health changed", without)
	}
	if (Check{Component: "workboard_scheduler", ID: "private-board", Status: "healthy", Code: "supervisor_ok"}).Validate() == nil {
		t.Fatal("scheduler identity admitted")
	}
	for _, malformed := range []Check{
		{Component: "workboard_scheduler", Status: "healthy", Code: "available"},
		{Component: "workboard_scheduler", Status: "disabled", Code: "disabled_by_policy"},
	} {
		if malformed.Validate() == nil {
			t.Fatal("non-supervisor scheduler state admitted", malformed)
		}
	}
}
