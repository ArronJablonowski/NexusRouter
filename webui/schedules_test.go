package webui

import (
	"testing"
	"time"
)

func TestSchedulePageBounds(t *testing.T) {
	p := SchedulePage{Version: 1, ObservedAt: time.Now(), Items: []Schedule{{ID: "health", Description: "Record health", Interval: "30s"}}}
	if p.Validate() != nil {
		t.Fatal("valid")
	}
	p.Items = append(p.Items, p.Items[0])
	if p.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	p.Items = p.Items[:1]
	p.Items[0].Interval = "-1s"
	if p.Validate() == nil {
		t.Fatal("invalid interval accepted")
	}
}

func TestScheduleAIUsageContract(t *testing.T) {
	p := SchedulePage{Version: 1, ObservedAt: time.Now(), Items: []Schedule{{ID: "job", Description: "Job", Interval: "1m"}}}
	for _, value := range []string{"", "unknown", "none", "possible"} {
		p.Items[0].AIUsage = value
		if p.Validate() != nil {
			t.Fatal(value)
		}
	}
	p.Items[0].AIUsage = "never-guessed"
	if p.Validate() == nil {
		t.Fatal("invalid AI usage accepted")
	}
}
