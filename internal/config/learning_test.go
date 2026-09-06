package config

import (
	"math"
	"strings"
	"testing"
)

func learningSettings() Settings {
	s := Defaults()
	cost := 0.1
	s.Skills.Root, s.Skills.Scope = "/skills", "project"
	s.Skills.GenerationBudget.Enabled, s.Skills.GenerationBudget.MaxCost = true, 1
	s.Skills.Learning.Enabled, s.Skills.Learning.ModelID, s.Skills.Learning.MaxCost = true, "local", 0.2
	s.Providers = []Provider{{ID: "ollama", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	s.Models = []Model{{ID: "local", Provider: "ollama", Model: "model", Locality: "local", Capabilities: []string{"general"}, ContextTokens: 4096, EstimatedCost: &cost, RAMBytes: 1024}}
	return s
}

func TestLearningValidation(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := learningSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Settings){
		"name":           func(s *Settings) { s.Skills.Learning.Name = "bad/name" },
		"domain":         func(s *Settings) { s.Skills.Learning.Domain = strings.Repeat("a", 65) },
		"model":          func(s *Settings) { s.Skills.Learning.ModelID = "" },
		"disabled shape": func(s *Settings) { s.Skills.Learning.Enabled = false; s.Skills.Learning.Interval = "0s" },
		"interval":       func(s *Settings) { s.Skills.Learning.Interval = "25h" },
		"cost":           func(s *Settings) { s.Skills.Learning.MaxCost = math.NaN() },
		"limit":          func(s *Settings) { s.Skills.Learning.ScanLimit = 21 },
		"skills":         func(s *Settings) { s.Skills.Enabled = false },
		"draft":          func(s *Settings) { s.Skills.AutoDraft = false },
		"budget":         func(s *Settings) { s.Skills.GenerationBudget.Enabled = false },
		"budget ceiling": func(s *Settings) { s.Skills.Learning.MaxCost = 2 },
		"model ceiling":  func(s *Settings) { s.Skills.Learning.MaxCost = 0 },
		"root":           func(s *Settings) { s.Skills.Root = ""; s.Skills.Scope = "" },
		"otel":           func(s *Settings) { s.Telemetry.OTEL = true },
		"context":        func(s *Settings) { s.Models[0].ContextTokens = 0 },
		"unknown cost":   func(s *Settings) { s.Models[0].EstimatedCost = nil },
		"ram":            func(s *Settings) { s.Models[0].RAMBytes = 0 },
		"privacy":        func(s *Settings) { s.Models[0].Locality = "cloud" },
		"mode":           func(s *Settings) { s.Mode = "cloud_only" },
		"unsupported generation adapter": func(s *Settings) {
			s.Skills.LocalOnly = false
			s.Models[0].Model, s.Models[0].Locality = "gpt-5.6-sol", "cloud"
			s.Providers[0].Kind, s.Providers[0].Endpoint, s.Providers[0].Executable = "unsupported", "", "/bin/codex"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := learningSettings()
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	s := learningSettings()
	s.Mode = "cloud_only"
	s.Skills.LocalOnly = false
	s.Models[0].Locality = "cloud"
	s.Models[0].RAMBytes = 0
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLearningLayering(t *testing.T) {
	s, err := Load(Options{UserFile: file(t, "skills:\n  learning:\n    name: user\n    interval: 2m\n"), ProjectFile: file(t, "skills:\n  learning:\n    name: project\n"), Env: Environment([]string{"DARWIN__SKILLS__LEARNING__NAME=env"}), Flags: map[string]string{"skills.learning.name": "flag"}})
	if err != nil || s.Skills.Learning.Name != "flag" || s.Skills.Learning.Interval != "2m" || s.Skills.Learning.Enabled {
		t.Fatalf("settings=%+v error=%v", s.Skills.Learning, err)
	}
}
