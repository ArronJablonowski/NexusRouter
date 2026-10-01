package health

import "testing"

func TestRemoteReviewWorkerGatesReadiness(t *testing.T) {
	base := []Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "model", ID: "fixture", Status: "healthy", Code: "available"}}
	for _, test := range []struct {
		status, code string
		ready        bool
	}{{"healthy", "supervisor_ok", true}, {"disabled", "disabled_by_policy", true}, {"degraded", "supervisor_error", false}, {"unavailable", "supervisor_stopped", false}, {"unknown", "supervisor_starting", false}} {
		check := Check{Component: "remote_review", Status: test.status, Code: test.code}
		if err := check.Validate(); err != nil {
			t.Fatal(err)
		}
		_, ready := Outcome(append(append([]Check{}, base...), check))
		if ready != test.ready {
			t.Fatal(test, ready)
		}
	}
	if _, ready := Outcome(base); !ready {
		t.Fatal("legacy report without remote review changed")
	}
}
