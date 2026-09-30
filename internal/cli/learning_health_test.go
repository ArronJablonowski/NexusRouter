package cli

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
)

func TestDaemonLearningHealthMerge(t *testing.T) {
	report := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: []health.Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "local", Status: "healthy", Code: "available"}}}
	report.Status, report.Ready = health.Outcome(report.Checks)
	check := health.Check{Component: "learning", Status: "degraded", Code: "supervisor_error"}
	out, err := withLearningHealth(report, check)
	if err != nil || out.Ready || len(report.Checks) != 5 || len(out.Checks) != 6 || !report.Ready {
		t.Fatal("health merge mutated source or hid failure", err)
	}
	if _, err := withLearningHealth(out, check); err == nil {
		t.Fatal("duplicate learning observation admitted")
	}
	check.Code = "private backend error"
	if _, err := withLearningHealth(report, check); err == nil {
		t.Fatal("raw learning error relayed")
	}
	if learningReady(nil) {
		t.Fatal("missing learner marked ready")
	}
}
