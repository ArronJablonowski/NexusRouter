package cli

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func TestWorkboardSchedulerHealthConversionAndMerge(t *testing.T) {
	report := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: []health.Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "local", Status: "healthy", Code: "available"}}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	for _, state := range []app.WorkboardScheduleHealth{
		{Status: "healthy", Code: "supervisor_ok"},
		{Status: "unknown", Code: "supervisor_starting"},
		{Status: "degraded", Code: "supervisor_stalled"},
		{Status: "degraded", Code: "supervisor_error"},
		{Status: "unavailable", Code: "supervisor_stopping"},
		{Status: "unavailable", Code: "supervisor_stopped"},
	} {
		out, err := withWorkboardSchedulerHealth(report, state)
		if err != nil || out.Validate() != nil || out.Ready != (state.Status == "healthy") || !report.Ready || len(report.Checks) != 5 {
			t.Fatalf("scheduler health merge failed for %+v: %+v %v", state, out, err)
		}
		if len(out.Checks) != 6 || out.Checks[5] != (health.Check{Component: "workboard_scheduler", Status: state.Status, Code: state.Code}) {
			t.Fatal("scheduler health conversion drift", out.Checks)
		}
		if _, err = withWorkboardSchedulerHealth(out, state); err == nil {
			t.Fatal("duplicate scheduler health admitted")
		}
	}
	for _, malformed := range []app.WorkboardScheduleHealth{{Status: "healthy", Code: "supervisor_error"}, {Status: "private", Code: "supervisor_ok"}, {Status: "healthy", Code: "private"}} {
		if _, err := withWorkboardSchedulerHealth(report, malformed); err == nil {
			t.Fatal("malformed scheduler health admitted", malformed)
		}
	}
}
