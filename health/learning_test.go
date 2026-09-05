package health

import (
	"testing"
	"time"
)

func TestLearningHealthParticipatesInReadiness(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "worker", Status: "healthy", Code: "available"}}
	for _, check := range []Check{
		{Component: "learning", Status: "disabled", Code: "disabled_by_policy"},
		{Component: "learning", Status: "healthy", Code: "supervisor_ok"},
		{Component: "learning", Status: "healthy", Code: "learning_budget_wait"},
		{Component: "learning", Status: "unknown", Code: "supervisor_starting"},
		{Component: "learning", Status: "degraded", Code: "supervisor_error"},
		{Component: "learning", Status: "unavailable", Code: "supervisor_stopped"},
	} {
		r := Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: append(append([]Check(nil), base...), check)}
		r.Status, r.Ready = Outcome(r.Checks)
		want := check.Status == "healthy" || check.Status == "disabled"
		if r.Validate() != nil || r.Ready != want {
			t.Fatal("learning readiness lost", r)
		}
	}
	if (Check{Component: "learning", ID: "private-scope", Status: "healthy", Code: "supervisor_ok"}).Validate() == nil {
		t.Fatal("unnecessary learning identity admitted")
	}
	if (Check{Component: "provider", ID: "model", Status: "healthy", Code: "learning_budget_wait"}).Validate() == nil {
		t.Fatal("learning-only observation used for a provider")
	}
}
