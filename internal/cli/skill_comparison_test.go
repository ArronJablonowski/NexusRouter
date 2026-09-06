package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func comparisonCLIRequest() skills.ComparisonRequest {
	return skills.ComparisonRequest{Version: 1, ModelID: "local", Domain: "creative", Profile: "default", Name: "workflow", BaselineVersion: strings.Repeat("a", 32), CandidateVersion: strings.Repeat("b", 32), Source: evaluation.UserFeedback, MinSamples: 20, MinDrop: 0.1, Tasks: []string{"task"}}
}

func TestSkillComparisonCLIReadOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(dir, "task.db")
	cfg.Skills.Root = filepath.Join(dir, "absent-skills")
	cfg.Skills.Scope = "project"
	cfg.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "local", Provider: "provider", Model: "model", Locality: "local", ContextTokens: 4096, RAMBytes: 1, EstimatedCost: &zero, Capabilities: []string{"general"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(comparisonCLIRequest())
	args := []string{"skills", "compare", "--config", path}
	var out, diagnostic bytes.Buffer
	run := func() int {
		out.Reset()
		diagnostic.Reset()
		return RunWithInput(args, bytes.NewReader(request), &out, &diagnostic, "test")
	}
	if run() != 1 || out.Len() != 0 {
		t.Fatal("missing database admitted")
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created missing database", err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCanceled} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if err := db.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if run() != 0 {
		t.Fatal(diagnostic.String())
	}
	var report skills.ComparisonReport
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Validate() != nil || !report.AdvisoryOnly || report.Excluded["nonfinal_outcome"] != 1 {
		t.Fatal("bad advisory report", out.String())
	}
	if RunWithInput(args, bytes.NewReader(request), brokenWriter{}, &diagnostic, "test") != 1 {
		t.Fatal("writer failure ignored")
	}
	after, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("database mutated", err)
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("catalog created", err)
	}
	for _, flags := range [][]string{nil, {"--config", path, "operand"}, {"--config", path, "--config", path}, {"--config="}, {"--scope", "private-value"}, {"--config", path, "--scope", "private-value"}} {
		out.Reset()
		diagnostic.Reset()
		if RunWithInput(append([]string{"skills", "compare"}, flags...), bytes.NewReader(request), &out, &diagnostic, "test") != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-value") {
			t.Fatal("invalid flags admitted")
		}
	}
}

func TestSkillComparisonCLIStrictJSON(t *testing.T) {
	body, _ := json.Marshal(comparisonCLIRequest())
	valid := string(body)
	if _, err := decodeSkillComparison(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`null`, `[]`, valid + ` {}`, `{"version":1,` + valid[1:], strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"version":1`, `"version":null`, 1), strings.Replace(valid, `"version":1,`, "", 1),
		strings.Replace(valid, `"tasks":["task"]`, `"tasks":[null]`, 1), strings.Replace(valid, `"name":"workflow"`, `"name":"\ud800"`, 1),
		strings.Replace(valid, `"tasks":["task"]`, `"tasks":["task","task"]`, 1), strings.Replace(valid, `"version":1`, `"version":1,"unknown":"private-value"`, 1),
		valid + strings.Repeat(" ", skillComparisonInputLimit), strings.Replace(valid, `"version":1`, `"version":1.0`, 1),
	} {
		var out, diagnostic bytes.Buffer
		if code := runSkillComparison([]string{"--config", "absent"}, strings.NewReader(bad), &out, &diagnostic); code != 2 || out.Len() != 0 || diagnostic.String() != "skills compare: invalid request\n" {
			t.Fatal("malformed input admitted", code, diagnostic.String())
		}
	}
	if _, err := decodeSkillComparison(nil); err == nil {
		t.Fatal("nil reader admitted")
	}
}
