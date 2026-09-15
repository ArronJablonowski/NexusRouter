package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOutcomeRollbackOptInAndBindings(t *testing.T) {
	base := Defaults()
	if base.Skills.OutcomeRollback || base.Validate() != nil {
		t.Fatal("unsafe default")
	}
	base.Skills.OutcomeRollback = true
	base.Skills.Root = "/catalog"
	base.Skills.Scope = "project"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Settings){
		"disabled": func(s *Settings) { s.Skills.Enabled = false }, "rollback disabled": func(s *Settings) { s.Skills.Rollback = false },
		"missing scope": func(s *Settings) { s.Skills.Scope = "" }, "missing root": func(s *Settings) { s.Skills.Root = "" },
		"missing both": func(s *Settings) { s.Skills.Root = ""; s.Skills.Scope = "" }, "relative": func(s *Settings) { s.Skills.Root = "relative" },
		"invalid scope": func(s *Settings) { s.Skills.Scope = "bad/scope" }, "broad root": func(s *Settings) { s.Skills.Root = "/" },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
	base.Skills.AutoActivate = false
	base.Skills.AutoDraft = false
	if err := base.Validate(); err != nil {
		t.Fatal("independent rollback depends on drafting", err)
	}
}

func TestOutcomeRollbackJSONCompatibilityAndLayering(t *testing.T) {
	s := Defaults().Skills
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// Exact pre-feature bytes retain all existing fields and ordering.
	want := `{"learning":{"enabled":false,"name":"default","domain":"general","model_id":"","interval":"1m","max_cost":0,"scan_limit":20},"generation_budget":{"enabled":false,"window":"24h","max_cost":0,"max_attempts":10,"max_in_flight":1,"cooldown":"1h"},"outcome_rollback_supervisor":{"version":1,"enabled":false,"interval":"5m","model_id":"","domain":"unknown","profile":"default","source":"user_feedback","privacy":"local_only","min_samples":20,"min_drop":0.1,"tasks_per_version":20},"enabled":true,"auto_draft":true,"auto_activate_after_validation":true,"rollback_on_regression":true,"root":"","scope":"","local_only":true,"max_skills":3,"max_bytes":16384}`
	if string(body) != want {
		t.Fatal("default skills JSON changed", string(body))
	}
	s.OutcomeRollback = true
	body, err = json.Marshal(s)
	if err != nil || !bytes.Contains(body, []byte(`"outcome_rollback":true`)) {
		t.Fatal("opt-in missing from fingerprint")
	}
	for _, options := range []Options{
		{ProjectFile: file(t, "skills:\n  root: /catalog\n  scope: project\n  outcome_rollback: true\n")},
		{Env: Environment([]string{"DARWIN__SKILLS__ROOT=/catalog", "DARWIN__SKILLS__SCOPE=project", "DARWIN__SKILLS__OUTCOME_ROLLBACK=true"})},
		{Flags: map[string]string{"skills.root": "/catalog", "skills.scope": "project", "skills.outcome_rollback": "true"}},
	} {
		got, err := Load(options)
		if err != nil || !got.Skills.OutcomeRollback {
			t.Fatal("opt-in unavailable", err)
		}
	}
}
