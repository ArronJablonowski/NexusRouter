package health

import (
	"testing"
	"time"
)

func TestReadinessRequiresOperationalCoreAndModel(t *testing.T) {
	checks := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "chat", Status: "healthy", Code: "available"}}
	for _, change := range []string{"none", "gpu_unknown", "database", "supervisor", "model"} {
		r := Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: append([]Check{}, checks...)}
		switch change {
		case "gpu_unknown":
			r.Checks = append(r.Checks, Check{Component: "resources", ID: "gpu", Status: "unknown", Code: "metrics_unknown"})
		case "database":
			r.Checks[1].Status = "unavailable"
			r.Checks[1].Code = "unavailable"
		case "supervisor":
			r.Checks[2].Status = "degraded"
			r.Checks[2].Code = "supervisor_error"
		case "model":
			r.Checks[4].Status = "unavailable"
			r.Checks[4].Code = "model_missing"
		}
		r.Status, r.Ready = Outcome(r.Checks)
		if r.Validate() != nil || r.Ready != (change == "none" || change == "gpu_unknown") {
			t.Fatal(change, r)
		}
		if change == "gpu_unknown" && r.Status != "degraded" {
			t.Fatal(r)
		}
		r.Ready = !r.Ready
		if r.Validate() == nil {
			t.Fatal("accepted contradictory readiness")
		}
	}
}

func TestHealthCheckRejectsContradictionAndRawErrors(t *testing.T) {
	for _, c := range []Check{
		{Component: "provider", ID: "p", Status: "healthy", Code: "unavailable"},
		{Component: "provider", ID: "p", Status: "unavailable", Code: "private-credential"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_stopped"},
		{Component: "resources", ID: "gpu", Status: "healthy", Code: "metrics_unknown"},
		{Component: "database", ID: "private/path", Status: "healthy", Code: "available"},
	} {
		if c.Validate() == nil {
			t.Fatal(c)
		}
	}
}
