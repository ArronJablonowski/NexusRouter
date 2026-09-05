package config

import (
	"math"
	"testing"
)

func TestDelegationReadToolsConfig(t *testing.T) {
	d := Defaults()
	if d.Workers.DelegateReadTools || d.Workers.DelegateMaxTurns != 4 {
		t.Fatal(d.Workers)
	}
	for _, tc := range []struct {
		name   string
		change func(*Settings)
		valid  bool
	}{
		{"enabled", func(s *Settings) {}, true},
		{"parent-disabled", func(s *Settings) { s.Tools.Enabled = false }, false},
		{"delegate-disabled", func(s *Settings) { s.Workers.DelegateModel = "" }, false},
		{"cloud", func(s *Settings) { s.Models[0].Locality = "cloud" }, false},
		{"zero-turns", func(s *Settings) { s.Workers.DelegateMaxTurns = 0 }, false},
		{"one-tool-turn", func(s *Settings) { s.Workers.DelegateMaxTurns = 1 }, false},
		{"two-turns", func(s *Settings) { s.Workers.DelegateMaxTurns = 2 }, true},
		{"eight-turns", func(s *Settings) { s.Workers.DelegateMaxTurns = 8 }, true},
		{"nine-turns", func(s *Settings) { s.Workers.DelegateMaxTurns = 9 }, false},
		{"one-inference-turn", func(s *Settings) { s.Workers.DelegateReadTools = false; s.Workers.DelegateMaxTurns = 1 }, true},
		{"budget-exact", func(s *Settings) { v := 0.25; s.Models[0].EstimatedCost = &v; s.Workers.DelegateMaxCost = 1 }, true},
		{"budget-too-small", func(s *Settings) { v := 0.25; s.Models[0].EstimatedCost = &v; s.Workers.DelegateMaxCost = 0.99 }, false},
		{"inference-cost-only", func(s *Settings) {
			v := 1.0
			s.Models[0].EstimatedCost = &v
			s.Workers.DelegateMaxCost = 1
			s.Workers.DelegateReadTools = false
		}, true},
		{"overflow-cost", func(s *Settings) {
			v := math.MaxFloat64
			s.Models[0].EstimatedCost = &v
			s.Workers.DelegateMaxCost = math.MaxFloat64
		}, false},
		{"large-exact-cost", func(s *Settings) {
			v := math.MaxFloat64 / 4
			s.Models[0].EstimatedCost = &v
			s.Workers.DelegateMaxCost = math.MaxFloat64
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := delegationSettings()
			s.Tools.Enabled = true
			s.Tools.ReadRoot = t.TempDir()
			s.Workers.DelegateReadTools = true
			tc.change(&s)
			if err := s.Validate(); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestDelegationReadToolsLayeredConfig(t *testing.T) {
	s, err := Load(Options{ProjectFile: file(t, "workers:\n  delegate_read_tools: false\n  delegate_max_turns: 2\n"), Env: map[string]string{"workers.delegate_max_turns": "3"}, Flags: map[string]string{"workers.delegate_max_turns": "8"}})
	if err != nil || s.Workers.DelegateMaxTurns != 8 || s.Workers.DelegateReadTools {
		t.Fatal(s.Workers, err)
	}
	for _, v := range []string{"0", "9", "1.5", "'4'", "true"} {
		if _, err := Load(Options{ProjectFile: file(t, "workers:\n  delegate_max_turns: "+v+"\n")}); err == nil {
			t.Fatal("accepted", v)
		}
	}
	if _, err := Load(Options{Flags: map[string]string{"workers.delegate_read_tools": "true"}}); err == nil {
		t.Fatal("accepted tools without delegate")
	}
}
