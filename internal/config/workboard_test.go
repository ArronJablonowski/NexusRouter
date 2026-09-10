package config

import "testing"

func TestWorkboardSchedulerDefaultsAndOverrides(t *testing.T) {
	defaults := Defaults()
	if !defaults.Workboard.Enabled || defaults.Workboard.Scheduler.Enabled || defaults.Workboard.Scheduler.Interval != "5s" ||
		defaults.Workboard.Scheduler.MaxActiveClaims != 3 || defaults.Workboard.Scheduler.CardScanLimit != 10000 {
		t.Fatalf("unexpected workboard defaults: %+v", defaults.Workboard)
	}
	settings, err := Load(Options{ProjectFile: file(t, "workboard:\n  scheduler:\n    interval: 250ms\n    max_active_claims: 2\n    card_scan_limit: 1\n")})
	if err != nil || settings.Workboard.Scheduler.Interval != "250ms" || settings.Workboard.Scheduler.MaxActiveClaims != 2 || settings.Workboard.Scheduler.CardScanLimit != 1 {
		t.Fatalf("valid workboard override rejected: %+v %v", settings.Workboard, err)
	}
	settings, err = Load(Options{Env: map[string]string{"workboard.scheduler.enabled": "false", "workboard.scheduler.card_scan_limit": "25"}})
	if err != nil || settings.Workboard.Scheduler.Enabled || settings.Workboard.Scheduler.CardScanLimit != 25 {
		t.Fatalf("valid workboard scalar overrides rejected: %+v %v", settings.Workboard, err)
	}
}

func TestWorkboardSchedulerValidation(t *testing.T) {
	tests := map[string]func(*Settings){
		"feature disabled": func(s *Settings) { s.Workboard.Enabled = false },
		"short interval":   func(s *Settings) { s.Workboard.Scheduler.Interval = "249ms" },
		"long interval":    func(s *Settings) { s.Workboard.Scheduler.Interval = "24h1ns" },
		"invalid interval": func(s *Settings) { s.Workboard.Scheduler.Interval = "invalid" },
		"zero claims":      func(s *Settings) { s.Workboard.Scheduler.MaxActiveClaims = 0 },
		"too many claims":  func(s *Settings) { s.Workboard.Scheduler.MaxActiveClaims = 65 },
		"worker overflow": func(s *Settings) {
			s.Workboard.Scheduler.Enabled = true
			s.Workboard.Scheduler.MaxActiveClaims = s.Workers.Max + 1
		},
		"zero scan":  func(s *Settings) { s.Workboard.Scheduler.CardScanLimit = 0 },
		"large scan": func(s *Settings) { s.Workboard.Scheduler.CardScanLimit = 10001 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := Defaults()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid workboard scheduler settings accepted")
			}
		})
	}
	for _, interval := range []string{"250ms", "24h"} {
		s := Defaults()
		s.Workboard.Scheduler.Interval = interval
		if err := s.Validate(); err != nil {
			t.Fatalf("boundary interval %q rejected: %v", interval, err)
		}
	}
}

func TestWorkboardSchedulerSchemaIsStrict(t *testing.T) {
	for _, body := range []string{
		"workboard:\n  unknown: true\n",
		"workboard:\n  scheduler:\n    unknown: true\n",
		"workboard:\n  scheduler:\n    max_active_claims: 2.5\n",
		"workboard:\n  scheduler:\n    card_scan_limit: \"10\"\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, body)}); err == nil {
			t.Fatalf("invalid workboard schema accepted: %q", body)
		}
	}
}
