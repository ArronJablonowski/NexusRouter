package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func TestServeConfiguredLearningPreflight(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	token := strings.Repeat("configured-learning-fixture-", 2)
	t.Setenv("DARWIN_API_TOKEN", token)
	for _, test := range []struct {
		name               string
		selected, injected bool
		want               string
	}{
		{"missing registry", true, false, "cannot prepare skill learning supervisor\n"},
		{"empty registry", true, false, "cannot prepare skill learning supervisor\n"},
		{"injected registry", true, true, "cannot bind daemon address\n"},
		{"draft only", false, false, "cannot bind daemon address\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s := config.Defaults()
			s.Daemon.Listen = listener.Addr().String()
			s.Telemetry.Database = filepath.Join(dir, "absent", "tasks.db")
			s.Skills.Root, s.Skills.Scope = filepath.Join(dir, "skills"), "project"
			s.Skills.GenerationBudget.Enabled, s.Skills.GenerationBudget.MaxCost = true, 1
			s.Skills.Learning.Enabled, s.Skills.Learning.ModelID, s.Skills.Learning.MaxCost = true, "local", 0.2
			cost := 0.1
			s.Providers = []config.Provider{{ID: "ollama", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
			s.Models = []config.Model{{ID: "local", Provider: "ollama", Model: "model", Locality: "local", Capabilities: []string{"general"}, ContextTokens: 4096, EstimatedCost: &cost, RAMBytes: 1024}}
			if test.selected {
				s.Skills.Learning.ValidatorID = "private-policy-v1"
			}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			body, err := yaml.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			var registry *skills.ValidatorRegistry
			calls := 0
			if test.injected {
				registry, err = skills.NewValidatorRegistry(map[string]skills.Validator{"private-policy-v1": skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) { calls++; return skills.Evidence{}, nil })})
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "empty registry" {
				registry, err = skills.NewValidatorRegistry(nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostic bytes.Buffer
			var code int
			if test.name == "missing registry" {
				code = runServe([]string{"--config", path}, &out, &diagnostic)
			} else {
				code = runServeWithValidators([]string{"--config", path}, &out, &diagnostic, registry)
			}
			if code != 1 || out.Len() != 0 || diagnostic.String() != test.want || calls != 0 {
				t.Fatal("unexpected preflight result", code, diagnostic.String(), calls)
			}
			for _, absent := range []string{filepath.Dir(s.Telemetry.Database), s.Skills.Root} {
				if _, err := os.Stat(absent); !os.IsNotExist(err) {
					t.Fatal("preflight created storage", err)
				}
			}
		})
	}
}

func TestConfiguredLearningHealth(t *testing.T) {
	s, err := app.NewService(config.Defaults(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.PrepareConfiguredLearning(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := plan.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	if configuredLearningReady(nil) || !configuredLearningReady(bundle) || len(bundle.Health()) != 1 {
		t.Fatal("disabled compatibility failed")
	}
	base := health.Report{Version: 1, CheckedAt: time.Now().UTC(), Checks: []health.Check{{Component: "daemon", Status: "healthy", Code: "serving"}, {Component: "database", Status: "healthy", Code: "available"}, {Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}, {Component: "resources", Status: "healthy", Code: "capacity_available"}, {Component: "model", ID: "local", Status: "healthy", Code: "available"}}}
	base.Status, base.Ready = health.Outcome(base.Checks)
	for _, regression := range []health.Check{
		{Component: "skill_regression", Status: "healthy", Code: "supervisor_ok"},
		{Component: "skill_regression", Status: "degraded", Code: "supervisor_error"},
		{Component: "skill_regression", Status: "unavailable", Code: "supervisor_stopped"},
	} {
		checks := []health.Check{{Component: "learning", Status: "healthy", Code: "supervisor_ok"}, regression}
		report, err := withConfiguredLearningHealth(base, checks)
		if err != nil || report.Ready != (regression.Status == "healthy") || len(report.Checks) != 7 || len(base.Checks) != 5 {
			t.Fatal("bundle health lost status or mutated input", err)
		}
		if _, err := withConfiguredLearningHealth(report, checks); err == nil {
			t.Fatal("duplicate bundle accepted")
		}
	}
	for _, checks := range [][]health.Check{nil, {{Component: "skill_regression", Status: "healthy", Code: "supervisor_ok"}}, {{Component: "learning", Status: "healthy", Code: "supervisor_ok"}, {Component: "learning", Status: "healthy", Code: "supervisor_ok"}}} {
		if _, err := withConfiguredLearningHealth(base, checks); err == nil {
			t.Fatal("invalid bundle accepted")
		}
	}
}
