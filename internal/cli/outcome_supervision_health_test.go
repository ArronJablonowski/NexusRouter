package cli

import (
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
)

func TestConfiguredOutcomeSupervisionHealthComposition(t *testing.T) {
	base := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", Status: "healthy", Code: "capacity_available"},
		{Component: "model", ID: "local", Status: "healthy", Code: "available"},
	}}
	base.Status, base.Ready = health.Outcome(base.Checks)
	for _, check := range []health.Check{
		{Component: "outcome_supervision", Status: "healthy", Code: "supervisor_ok"},
		{Component: "outcome_supervision", Status: "disabled", Code: "disabled_by_policy"},
		{Component: "outcome_supervision", Status: "degraded", Code: "supervisor_error"},
	} {
		report, err := withConfiguredOutcomeSupervisionHealth(base, check)
		wantReady := check.Status == "healthy" || check.Status == "disabled"
		if err != nil || report.Ready != wantReady || len(report.Checks) != len(base.Checks)+1 || len(base.Checks) != 5 {
			t.Fatal(check, report, err)
		}
		if _, err = withConfiguredOutcomeSupervisionHealth(report, check); err == nil {
			t.Fatal("duplicate outcome supervisor accepted")
		}
	}
	for _, check := range []health.Check{
		{},
		{Component: "learning", Status: "healthy", Code: "supervisor_ok"},
		{Component: "outcome_supervision", ID: "unexpected", Status: "healthy", Code: "supervisor_ok"},
	} {
		if _, err := withConfiguredOutcomeSupervisionHealth(base, check); err == nil {
			t.Fatal("invalid outcome health accepted", check)
		}
	}
}
