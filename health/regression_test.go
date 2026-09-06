package health

import (
	"testing"
	"time"
)

func TestRegressionMonitorReadinessAndIdentity(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "worker", Status: "healthy", Code: "available"}}
	for _, observed := range []Check{
		{Component: "skill_regression", Status: "healthy", Code: "supervisor_ok"},
		{Component: "skill_regression", Status: "unknown", Code: "supervisor_starting"},
		{Component: "skill_regression", Status: "degraded", Code: "supervisor_error"},
		{Component: "skill_regression", Status: "degraded", Code: "supervisor_stalled"},
		{Component: "skill_regression", Status: "unavailable", Code: "supervisor_stopped"},
	} {
		for _, reverse := range []bool{false, true} {
			optional := []Check{observed, {Component: "learning", Status: "healthy", Code: "supervisor_ok"}}
			if reverse {
				optional[0], optional[1] = optional[1], optional[0]
			}
			r := Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: append(append([]Check(nil), base...), optional...)}
			r.Status, r.Ready = Outcome(r.Checks)
			if r.Validate() != nil || r.Ready != (observed.Status == "healthy") {
				t.Fatal("monitor readiness ignored or order-dependent", r)
			}
		}
	}
	if (Check{Component: "skill_regression", ID: "private-skill", Status: "healthy", Code: "supervisor_ok"}).Validate() == nil {
		t.Fatal("private monitor identity admitted")
	}
	if (Check{Component: "skill_regression", Status: "healthy", Code: "learning_budget_wait"}).Validate() == nil {
		t.Fatal("learning-specific code admitted")
	}
}
