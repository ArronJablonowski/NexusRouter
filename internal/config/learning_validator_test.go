package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLearningValidatorSelectionValidation(t *testing.T) {
	base := learningSettings()
	base.Skills.Learning.ValidatorID = "objective-v1"
	base.Skills.Learning.RegressionName = "regression"
	base.Skills.Learning.RegressionInterval = "1s"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, duration := range []string{"1s", "24h"} {
		s := base
		s.Skills.Learning.RegressionInterval = duration
		if err := s.Validate(); err != nil {
			t.Fatal("valid regression interval", err)
		}
	}
	for name, mutate := range map[string]func(*Settings){
		"validator shape":     func(s *Settings) { s.Skills.Learning.ValidatorID = "bad/name" },
		"validator length":    func(s *Settings) { s.Skills.Learning.ValidatorID = strings.Repeat("x", 65) },
		"regression shape":    func(s *Settings) { s.Skills.Learning.RegressionName = "bad.name" },
		"name absent":         func(s *Settings) { s.Skills.Learning.RegressionName = "" },
		"interval absent":     func(s *Settings) { s.Skills.Learning.RegressionInterval = "" },
		"validator absent":    func(s *Settings) { s.Skills.Learning.ValidatorID = "" },
		"interval short":      func(s *Settings) { s.Skills.Learning.RegressionInterval = "999ms" },
		"interval long":       func(s *Settings) { s.Skills.Learning.RegressionInterval = "24h1s" },
		"interval malformed":  func(s *Settings) { s.Skills.Learning.RegressionInterval = "later" },
		"activation disabled": func(s *Settings) { s.Skills.AutoActivate = false },
		"rollback disabled":   func(s *Settings) { s.Skills.Rollback = false },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	disabled := base
	disabled.Skills.Learning.Enabled = false
	disabled.Skills.Enabled = false
	disabled.Skills.AutoActivate = false
	disabled.Skills.Rollback = false
	if err := disabled.Validate(); err != nil {
		t.Fatal("kill switch cannot retain selection", err)
	}
	disabled.Skills.Learning.RegressionInterval = "bad"
	if disabled.Validate() == nil {
		t.Fatal("disabled malformed selection accepted")
	}
	legacy := learningSettings()
	legacy.Skills.AutoActivate = false
	legacy.Skills.Rollback = false
	if err := legacy.Validate(); err != nil {
		t.Fatal("legacy draft-only policy rejected", err)
	}
}

func TestLearningValidatorLayeringAndDefaultJSONCompatibility(t *testing.T) {
	defaults := Defaults().Skills.Learning
	got, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"enabled":false,"name":"default","domain":"general","model_id":"","interval":"1m","max_cost":0,"scan_limit":20}`)
	if !bytes.Equal(got, want) {
		t.Fatal("default serialized learning policy changed", string(got))
	}
	settings, err := Load(Options{
		UserFile:    file(t, "skills:\n  learning:\n    validator_id: user\n    regression_name: monitor\n    regression_interval: 2m\n"),
		ProjectFile: file(t, "skills:\n  learning:\n    validator_id: project\n    regression_interval: 3m\n"),
		Env:         Environment([]string{"DARWIN__SKILLS__LEARNING__VALIDATOR_ID=env", "DARWIN__SKILLS__LEARNING__REGRESSION_INTERVAL=4m"}),
		Flags:       map[string]string{"skills.learning.validator_id": "flag", "skills.learning.regression_interval": "5m"},
	})
	if err != nil || settings.Skills.Learning.ValidatorID != "flag" || settings.Skills.Learning.RegressionName != "monitor" || settings.Skills.Learning.RegressionInterval != "5m" || settings.Skills.Learning.Enabled {
		t.Fatal("selection layering failed", settings.Skills.Learning, err)
	}
	encoded, err := json.Marshal(settings.Skills.Learning)
	if err != nil || !bytes.Contains(encoded, []byte(`"validator_id":"flag"`)) || !bytes.Contains(encoded, []byte(`"regression_name":"monitor"`)) {
		t.Fatal("explicit selection absent from policy", err)
	}
}
