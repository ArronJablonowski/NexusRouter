package health

import (
	"testing"
	"time"
)

func TestTraceExportHealthSupplemental(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "worker", Status: "healthy", Code: "available"}}
	for _, check := range []Check{{Component: "trace_export", Status: "healthy", Code: "supervisor_ok"}, {Component: "trace_export", Status: "disabled", Code: "disabled_by_policy"}, {Component: "trace_export", Status: "unknown", Code: "supervisor_starting"}, {Component: "trace_export", Status: "degraded", Code: "supervisor_error"}, {Component: "trace_export", Status: "degraded", Code: "supervisor_stalled"}, {Component: "trace_export", Status: "unavailable", Code: "supervisor_stopped"}} {
		checks := append(append([]Check(nil), base...), check)
		status, ready := Outcome(checks)
		want := "degraded"
		if check.Status == "healthy" || check.Status == "disabled" {
			want = "healthy"
		}
		report := Report{Version: 1, CheckedAt: time.Now(), Status: status, Ready: ready, Checks: checks}
		if check.Validate() != nil || report.Validate() != nil || !ready || status != want {
			t.Fatal(report)
		}
	}
	if (Check{Component: "trace_export", ID: "private-endpoint", Status: "healthy", Code: "supervisor_ok"}).Validate() == nil {
		t.Fatal("private trace exporter identity accepted")
	}
}
