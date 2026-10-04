package app

import (
	"context"
	"testing"
)

func TestBrowserSchedulesIsReadOnlyConfiguration(t *testing.T) {
	s := submissionService(t)
	p, e := s.BrowserSchedules(context.Background())
	if e != nil || p.Validate() != nil {
		t.Fatalf("schedule configuration: %v", e)
	}
	found := false
	for _, row := range p.Items {
		if row.ID == "workboards" {
			found = true
			if row.Enabled != s.settings.Workboard.Scheduler.Enabled {
				t.Fatal("enabled state")
			}
		}
	}
	if !found {
		t.Fatal("workboard schedule missing")
	}
}
