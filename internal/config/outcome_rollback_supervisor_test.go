package config

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func outcomeRollbackSupervisorSettings() Settings {
	s := Defaults()
	s.Skills.Root, s.Skills.Scope = "/catalog", "project"
	s.Skills.OutcomeRollback = true
	s.Skills.OutcomeRollbackSupervisor.Enabled = true
	s.Skills.OutcomeRollbackSupervisor.ModelID = "worker"
	s.Providers = []Provider{{ID: "local", Kind: "ollama"}}
	s.Models = []Model{{ID: "worker", Provider: "local", Model: "model", Locality: "local", Capabilities: []string{"chat"}}}
	return s
}

func TestOutcomeRollbackSupervisorDefaultsAndBindings(t *testing.T) {
	defaults := Defaults()
	p := defaults.Skills.OutcomeRollbackSupervisor
	if p.Enabled || p.Version != 1 || p.Interval != "5m" || p.ModelID != "" || p.Domain != "unknown" || p.Profile != "default" || p.Source != "user_feedback" || p.Privacy != "local_only" || p.MinSamples != 20 || p.MinDrop != .1 || p.TasksPerVersion != 20 {
		t.Fatalf("unsafe or incomplete defaults: %+v", p)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := outcomeRollbackSupervisorSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Settings){
		"skills disabled":    func(s *Settings) { s.Skills.Enabled = false },
		"rollback disabled":  func(s *Settings) { s.Skills.Rollback = false },
		"operation disabled": func(s *Settings) { s.Skills.OutcomeRollback = false },
		"missing root":       func(s *Settings) { s.Skills.Root, s.Skills.Scope = "", "" },
		"missing scope":      func(s *Settings) { s.Skills.Scope = "" },
		"missing model":      func(s *Settings) { s.Skills.OutcomeRollbackSupervisor.ModelID = "" },
		"unknown model":      func(s *Settings) { s.Skills.OutcomeRollbackSupervisor.ModelID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			s := outcomeRollbackSupervisorSettings()
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("unbound supervisor accepted")
			}
		})
	}
}

func TestOutcomeRollbackSupervisorStrictPolicy(t *testing.T) {
	for name, mutate := range map[string]func(*OutcomeRollbackSupervisor){
		"version":            func(p *OutcomeRollbackSupervisor) { p.Version = 2 },
		"short interval":     func(p *OutcomeRollbackSupervisor) { p.Interval = "999ms" },
		"long interval":      func(p *OutcomeRollbackSupervisor) { p.Interval = "25h" },
		"invalid interval":   func(p *OutcomeRollbackSupervisor) { p.Interval = "often" },
		"model":              func(p *OutcomeRollbackSupervisor) { p.ModelID = "bad/model" },
		"domain":             func(p *OutcomeRollbackSupervisor) { p.Domain = "general" },
		"profile":            func(p *OutcomeRollbackSupervisor) { p.Profile = "bad/profile" },
		"source":             func(p *OutcomeRollbackSupervisor) { p.Source = "llm_judge" },
		"privacy":            func(p *OutcomeRollbackSupervisor) { p.Privacy = "public" },
		"few samples":        func(p *OutcomeRollbackSupervisor) { p.MinSamples = 19 },
		"many samples":       func(p *OutcomeRollbackSupervisor) { p.MinSamples = 101 },
		"zero drop":          func(p *OutcomeRollbackSupervisor) { p.MinDrop = 0 },
		"large drop":         func(p *OutcomeRollbackSupervisor) { p.MinDrop = 1.01 },
		"nan drop":           func(p *OutcomeRollbackSupervisor) { p.MinDrop = math.NaN() },
		"few tasks":          func(p *OutcomeRollbackSupervisor) { p.TasksPerVersion = 19 },
		"many tasks":         func(p *OutcomeRollbackSupervisor) { p.TasksPerVersion = 101 },
		"impossible samples": func(p *OutcomeRollbackSupervisor) { p.MinSamples = 21 },
	} {
		t.Run(name, func(t *testing.T) {
			s := outcomeRollbackSupervisorSettings()
			mutate(&s.Skills.OutcomeRollbackSupervisor)
			if s.Validate() == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}

	s := outcomeRollbackSupervisorSettings()
	for _, domain := range []string{"creative", "unknown"} {
		for _, source := range []string{"deterministic", "tool_result", "llm_judge"} {
			s.Skills.OutcomeRollbackSupervisor.Domain, s.Skills.OutcomeRollbackSupervisor.Source = domain, source
			if s.Validate() == nil {
				t.Fatalf("%s admitted for %s", source, domain)
			}
		}
	}
	for _, domain := range []string{"code", "coding", "debugging", "math", "structured_json"} {
		for _, source := range []string{"deterministic", "tool_result"} {
			s.Skills.OutcomeRollbackSupervisor.Domain, s.Skills.OutcomeRollbackSupervisor.Source = domain, source
			if err := s.Validate(); err != nil {
				t.Fatalf("%s rejected for %s: %v", source, domain, err)
			}
		}
		for _, source := range []string{"user_feedback", "llm_judge"} {
			s.Skills.OutcomeRollbackSupervisor.Source = source
			if s.Validate() == nil {
				t.Fatalf("%s admitted for %s", source, domain)
			}
		}
	}
}

func TestOutcomeRollbackSupervisorLayeringAndRedaction(t *testing.T) {
	s, err := Load(Options{
		UserFile:    file(t, "skills:\n  outcome_rollback_supervisor:\n    interval: 2m\n    domain: creative\n"),
		ProjectFile: file(t, "skills:\n  outcome_rollback_supervisor:\n    profile: project\n"),
		Env:         Environment([]string{"DARWIN__SKILLS__OUTCOME_ROLLBACK_SUPERVISOR__PRIVACY=cloud_allowed"}),
		Flags:       map[string]string{"skills.outcome_rollback_supervisor.min_drop": "0.2"},
	})
	p := s.Skills.OutcomeRollbackSupervisor
	if err != nil || p.Enabled || p.Interval != "2m" || p.Domain != "creative" || p.Profile != "project" || p.Privacy != "cloud_allowed" || p.MinDrop != .2 {
		t.Fatalf("settings=%+v error=%v", p, err)
	}

	s = outcomeRollbackSupervisorSettings()
	original := s.Skills.OutcomeRollbackSupervisor
	body, err := s.RedactedJSON()
	if err != nil || !bytes.Contains(body, []byte(`"outcome_rollback_supervisor"`)) || !bytes.Contains(body, []byte(`"model_id": "worker"`)) || !bytes.Contains(body, []byte(`"source": "user_feedback"`)) || bytes.Contains(body, []byte(`/catalog`)) || !bytes.Contains(body, []byte(`"root": "[REDACTED]"`)) {
		t.Fatal("incomplete or unsafe redaction", string(body), err)
	}
	if s.Skills.Root != "/catalog" || s.Skills.OutcomeRollbackSupervisor != original {
		t.Fatal("redaction mutated settings")
	}

	for _, raw := range []string{
		"skills:\n  outcome_rollback_supervisor:\n    version: 2\n",
		"skills:\n  outcome_rollback_supervisor:\n    source: llm_judge\n",
		"skills:\n  outcome_rollback_supervisor:\n    unknown: true\n",
		"skills:\n  outcome_rollback_supervisor:\n    min_samples: 20.0\n",
		"skills:\n  outcome_rollback_supervisor:\n    min_drop: .nan\n",
	} {
		if _, err := Load(Options{ProjectFile: file(t, raw)}); err == nil || strings.Contains(err.Error(), raw) {
			t.Fatal("malformed layer accepted or leaked", err)
		}
	}
}
