package health

import (
	"testing"
	"time"
)

func TestMetricsExportHealthSupplemental(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "worker", Status: "healthy", Code: "available"}}
	for _, check := range []Check{{Component: "metrics_export", Status: "healthy", Code: "supervisor_ok"}, {Component: "metrics_export", Status: "disabled", Code: "disabled_by_policy"}, {Component: "metrics_export", Status: "unknown", Code: "supervisor_starting"}, {Component: "metrics_export", Status: "degraded", Code: "supervisor_error"}, {Component: "metrics_export", Status: "degraded", Code: "supervisor_stalled"}, {Component: "metrics_export", Status: "unavailable", Code: "supervisor_stopped"}} {
		checks := append(append([]Check(nil), base...), check)
		status, ready := Outcome(checks)
		want := "degraded"
		if check.Status == "healthy" || check.Status == "disabled" {
			want = "healthy"
		}
		r := Report{Version: 1, CheckedAt: time.Now(), Status: status, Ready: ready, Checks: checks}
		if check.Validate() != nil || r.Validate() != nil || !ready || status != want {
			t.Fatal(r)
		}
	}
	if (Check{Component: "metrics_export", ID: "private-endpoint", Status: "healthy", Code: "supervisor_ok"}).Validate() == nil {
		t.Fatal("private exporter identity accepted")
	}
	status, ready := Outcome(base)
	if status != "healthy" || !ready {
		t.Fatal("legacy health changed")
	}
}
