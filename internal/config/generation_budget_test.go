package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestGenerationBudgetDefaultsAndLayers(t *testing.T) {
	want := GenerationBudget{Window: "24h", MaxAttempts: 10, MaxInFlight: 1, Cooldown: "1h"}
	if got := Defaults().Skills.GenerationBudget; got != want || got.Validate() != nil {
		t.Fatalf("defaults: %+v", got)
	}
	s, err := Load(Options{
		UserFile:    file(t, "skills:\n  generation_budget:\n    enabled: true\n    window: 2h\n    max_cost: 5\n    max_attempts: 8\n"),
		ProjectFile: file(t, "skills:\n  generation_budget:\n    max_cost: 3\n    cooldown: 0s\n"),
		Env:         Environment([]string{"DARWIN__SKILLS__GENERATION_BUDGET__MAX_ATTEMPTS=6"}),
		Flags:       map[string]string{"skills.generation_budget.max_in_flight": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = GenerationBudget{Enabled: true, Window: "2h", MaxCost: 3, MaxAttempts: 6, MaxInFlight: 2, Cooldown: "0s"}
	if s.Skills.GenerationBudget != want {
		t.Fatalf("layering: %+v", s.Skills.GenerationBudget)
	}
	body, err := s.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	var displayed Settings
	if json.Unmarshal(body, &displayed) != nil || displayed.Skills.GenerationBudget != want || s.Skills.GenerationBudget != want {
		t.Fatal("redacted display changed budget")
	}
}

func TestGenerationBudgetValidation(t *testing.T) {
	for _, change := range []func(*GenerationBudget){
		func(b *GenerationBudget) { b.Window = "59s" }, func(b *GenerationBudget) { b.Window = "31d" }, func(b *GenerationBudget) { b.Window = "bad-secret-duration" },
		func(b *GenerationBudget) { b.MaxCost = -1 }, func(b *GenerationBudget) { b.MaxCost = math.NaN() }, func(b *GenerationBudget) { b.MaxCost = math.Inf(1) },
		func(b *GenerationBudget) { b.MaxAttempts = 0 }, func(b *GenerationBudget) { b.MaxAttempts = 1001 }, func(b *GenerationBudget) { b.MaxInFlight = 0 }, func(b *GenerationBudget) { b.MaxInFlight = 11 },
		func(b *GenerationBudget) { b.Cooldown = "-1s" }, func(b *GenerationBudget) { b.Cooldown = "25h" }, func(b *GenerationBudget) { b.Cooldown = "invalid" },
	} {
		for _, enabled := range []bool{false, true} {
			s := Defaults()
			s.Skills.GenerationBudget.Enabled = enabled
			change(&s.Skills.GenerationBudget)
			if err := s.Validate(); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid budget accepted or leaked", err)
			}
		}
	}
	for _, window := range []string{"1m", "30d"} {
		b := Defaults().Skills.GenerationBudget
		b.Window = window
		b.Cooldown = window
		b.MaxAttempts = 1000
		b.MaxInFlight = 1000
		if err := b.Validate(); err != nil {
			t.Fatal("boundary rejected", err)
		}
	}
	for _, raw := range []string{"max_cost: .nan", "max_cost: .inf", "max_attempts: 1.5", "max_in_flight: nope", "enabled: maybe", "window: []", "cooldown: {}", "unexpected: true"} {
		if _, err := Load(Options{ProjectFile: file(t, "skills:\n  generation_budget:\n    "+raw+"\n")}); err == nil {
			t.Fatal("invalid config loaded", raw)
		}
	}
}
