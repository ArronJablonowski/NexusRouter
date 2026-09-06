package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func selectionCLIInput() skills.ComparisonSelectionRequest {
	r := comparisonCLIRequest()
	return skills.ComparisonSelectionRequest{Version: r.Version, ModelID: r.ModelID, Domain: r.Domain, Profile: r.Profile, Name: r.Name, BaselineVersion: r.BaselineVersion, CandidateVersion: r.CandidateVersion, Source: r.Source, MinSamples: r.MinSamples, MinDrop: r.MinDrop, Privacy: "local_only", TasksPerVersion: 20}
}

func TestSkillComparisonSelectionCLIReadOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(dir, "task.db")
	cfg.Skills.Scope = "project"
	cfg.Skills.Root = filepath.Join(dir, "absent-skills")
	zero := 0.0
	cfg.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	cfg.Models = []config.Model{{ID: "local", Provider: "provider", Model: "model", Locality: "local", ContextTokens: 4096, RAMBytes: 1, EstimatedCost: &zero, Capabilities: []string{"general"}}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(selectionCLIInput())
	args := []string{"skills", "compare-select", "--config", path}
	var out, diagnostic bytes.Buffer
	run := func() int {
		out.Reset()
		diagnostic.Reset()
		return RunWithInput(args, bytes.NewReader(input), &out, &diagnostic, "test")
	}
	if run() != 1 || out.Len() != 0 {
		t.Fatal("missing store admitted")
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created database")
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfg.Telemetry.Database)
	if run() != 0 {
		t.Fatal(diagnostic.String())
	}
	var report skills.ComparisonSelectionReport
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Validate() != nil || report.ConfiguredModelID != "local" || report.Comparison != nil || report.Watermark != 0 {
		t.Fatal("invalid empty selection report", out.String())
	}
	if RunWithInput(args, bytes.NewReader(input), brokenWriter{}, &diagnostic, "test") != 1 {
		t.Fatal("writer failure ignored")
	}
	after, _ := os.ReadFile(cfg.Telemetry.Database)
	if !bytes.Equal(before, after) {
		t.Fatal("database mutated")
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("catalog created")
	}
	for _, flags := range [][]string{nil, {"--config", path, "extra"}, {"--config", path, "--config", path}, {"--scope", "private-value"}, {"--config="}} {
		out.Reset()
		diagnostic.Reset()
		if code := RunWithInput(append([]string{"skills", "compare-select"}, flags...), bytes.NewReader(input), &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-value") {
			t.Fatal("invalid flags admitted")
		}
	}
}

func TestSkillComparisonSelectionCLIStrictJSON(t *testing.T) {
	body, _ := json.Marshal(selectionCLIInput())
	valid := string(body)
	if _, err := decodeSkillComparisonSelection(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`null`, valid + ` {}`, `{"version":1,` + valid[1:], strings.Replace(valid, `"version":1`, `"Version":1`, 1), strings.Replace(valid, `"version":1`, `"version":null`, 1), strings.Replace(valid, `"privacy":"local_only"`, `"privacy":"invalid"`, 1), strings.Replace(valid, `"domain":"creative"`, `"domain":"\ud800"`, 1), strings.Replace(valid, `"tasks_per_version":20`, `"tasks_per_version":19`, 1), strings.Replace(valid, `"tasks_per_version":20`, `"tasks_per_version":20,"tasks":[]`, 1), valid + strings.Repeat(" ", 64<<10)} {
		var out, diagnostic bytes.Buffer
		if code := runSkillComparisonSelection([]string{"--config", "absent"}, strings.NewReader(bad), &out, &diagnostic); code != 2 || out.Len() != 0 || diagnostic.String() != "skills compare-select: invalid request\n" {
			t.Fatal("malformed input admitted", code, diagnostic.String())
		}
	}
	if _, err := decodeSkillComparisonSelection(nil); err == nil {
		t.Fatal("nil input admitted")
	}
}
