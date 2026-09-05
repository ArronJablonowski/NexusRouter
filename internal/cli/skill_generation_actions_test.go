package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

type skillGenerationFailingOutput struct{ panic bool }

func (w skillGenerationFailingOutput) Write([]byte) (int, error) {
	if w.panic {
		panic("private-output-error")
	}
	return 0, errors.New("private-output-error")
}

func TestSkillGenerationActionsCLIEndToEnd(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := "A creative response"
		if calls.Add(1) > 2 {
			text = `{"version":1,"description":"Reusable workflow","tags":[],"steps":["Inspect user requirements"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Check requirements"]}`
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer server.Close()
	cfg := config.Defaults()
	// A cloud designation avoids host hardware dependence; HTTP stays on localhost.
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "generation.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "generator", Provider: "fixture", Model: "fixture", Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 32768, EstimatedCost: &zero}}
	var tasks []string
	for range 2 {
		result, err := app.RunExplicit(ctx, cfg, app.Request{ModelID: "generator", Prompt: "Write a creative response", Domain: "creative"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.RecordFeedback(ctx, cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, result.TaskID)
	}
	cfg.Skills.Enabled = true
	cfg.Skills.AutoDraft = true
	cfg.Skills.LocalOnly = false
	cfg.Skills.Scope = "project"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skills.Root = filepath.Join(root, "skills")
	configPath := filepath.Join(t.TempDir(), "generation.yaml")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var discoveryOut, discoveryErr bytes.Buffer
	if code := Run([]string{"skill-generations", "discover", "--config", configPath, "--domain", "creative"}, &discoveryOut, &discoveryErr, "test"); code != 0 {
		t.Fatal("discovery failed", code, discoveryErr.String())
	}
	var page skills.WorkflowCandidatePage
	if json.Unmarshal(discoveryOut.Bytes(), &page) != nil || page.Validate("", 20) != nil || len(page.Candidates) != 2 || calls.Load() != 2 {
		t.Fatal("invalid discovery or inference", discoveryOut.String())
	}
	tasks = nil
	for _, candidate := range page.Candidates {
		tasks = append(tasks, candidate.TaskID)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	generate := []string{"skill-generations", "generate", "--config", configPath, "--id", "generation", "--model", "generator", "--name", "workflow", "--tasks", strings.Join(tasks, ",")}
	if code := Run(generate, &out, &diagnostic, "test"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var attempt skills.GenerationAttempt
	if json.Unmarshal(out.Bytes(), &attempt) != nil || attempt.Status != "drafted" || attempt.Validate() != nil || calls.Load() != 3 {
		t.Fatal("invalid generated attempt", out.String(), calls.Load())
	}
	saved, err := db.SkillGenerationAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatal("generation not durable", err)
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("generation published implicitly", err)
	}
	after, err := db.SkillWorkflowSources(ctx, tasks)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source mutated", err)
	}
	out.Reset()
	diagnostic.Reset()
	if code := Run(generate, &out, &diagnostic, "test"); code != 1 || calls.Load() != 3 {
		t.Fatal("duplicate generation dispatched", code, calls.Load())
	}
	var first skills.Version
	for i := 0; i < 2; i++ {
		out.Reset()
		diagnostic.Reset()
		if code := Run([]string{"skill-generations", "publish", "--config", configPath, "--id", "generation"}, &out, &diagnostic, "test"); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		var v skills.Version
		if json.Unmarshal(out.Bytes(), &v) != nil || v.ID == "" {
			t.Fatal(out.String())
		}
		if i == 0 {
			first = v
		} else if !reflect.DeepEqual(first, v) {
			t.Fatal("publication retry changed version")
		}
	}
	store, err := skills.OpenReadOnly(cfg.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	active, err := store.Discover(ctx, "project", nil, 10)
	if err != nil || len(active) != 0 {
		t.Fatal("publication activated skill", active, err)
	}
	if calls.Load() != 3 {
		t.Fatal("publication invoked provider")
	}
	for i, panics := range []bool{false, true} {
		id := fmt.Sprintf("lost-output-%d", i)
		args := append([]string(nil), generate...)
		for j := range args {
			if args[j] == "generation" {
				args[j] = id
			}
		}
		diagnostic.Reset()
		if code := Run(args, skillGenerationFailingOutput{panics}, &diagnostic, "test"); code != 1 {
			t.Fatal("output failure acknowledged", code)
		}
		persisted, err := db.SkillGenerationAttempt(ctx, id)
		if err != nil || persisted.Status != "drafted" {
			t.Fatal("lost output lost generation", persisted, err)
		}
		count := calls.Load()
		out.Reset()
		diagnostic.Reset()
		if code := Run(args, &out, &diagnostic, "test"); code != 1 || calls.Load() != count {
			t.Fatal("lost output allowed redispatch", code)
		}
		publish := []string{"skill-generations", "publish", "--config", configPath, "--id", id}
		diagnostic.Reset()
		if code := Run(publish, skillGenerationFailingOutput{panics}, &diagnostic, "test"); code != 1 {
			t.Fatal("publication output failure acknowledged", code)
		}
		catalogBefore, err := os.ReadFile(filepath.Join(cfg.Skills.Root, "catalog.json"))
		if err != nil {
			t.Fatal(err)
		}
		out.Reset()
		diagnostic.Reset()
		if code := Run(publish, &out, &diagnostic, "test"); code != 0 {
			t.Fatal("saved publication retry failed", code, diagnostic.String())
		}
		var retry skills.Version
		if json.Unmarshal(out.Bytes(), &retry) != nil || retry.ID == "" {
			t.Fatal(out.String())
		}
		catalogAfter, err := os.ReadFile(filepath.Join(cfg.Skills.Root, "catalog.json"))
		if err != nil || !bytes.Equal(catalogBefore, catalogAfter) {
			t.Fatal("publication output failure caused second version", err)
		}
		if strings.Contains(diagnostic.String(), "private-output-error") || calls.Load() != count {
			t.Fatal("output failure leaked or dispatched")
		}
	}
}

func TestSkillGenerationActionsCLIRejectsFlagsBeforeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing-config.yaml")
	for _, command := range []string{"generate", "publish"} {
		base := []string{"skill-generations", command, "--config", path, "--id", "attempt"}
		if command == "generate" {
			base = append(base, "--model", "generator", "--name", "workflow", "--tasks", "task-a,task-b")
		}
		invalid := [][]string{{"--config", path}, {"--id=attempt"}, {"--scope", "project"}, {"--db", "private-db"}, {"--unknown", "private-secret"}, {"--id", "../private-secret"}}
		if command == "generate" {
			invalid = append(invalid, []string{"--max-cost", "-1"}, []string{"--max-cost", "NaN"}, []string{"--max-cost", "Inf"}, []string{"--max-cost", "private-secret"})
		} else {
			invalid = append(invalid, []string{"--model", "generator"}, []string{"--tasks", "task-a,task-b"}, []string{"--max-cost", "0"})
		}
		for _, extra := range invalid {
			var out, diagnostic bytes.Buffer
			args := append(append([]string(nil), base...), extra...)
			if code := Run(args, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-secret") || strings.Contains(diagnostic.String(), path) {
				t.Fatal("invalid flags not sanitized", args, code, diagnostic.String())
			}
		}
		var out, diagnostic bytes.Buffer
		if code := Run(base, &out, &diagnostic, "test"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), path) {
			t.Fatal("config failure not sanitized", code, diagnostic.String())
		}
	}
	for _, tasks := range []string{"task-a", "task-a,task-a", "task-a,", ",task-a", "task-a,../bad", strings.Repeat("a", 65) + ",task-b", strings.Join(strings.Fields("a b c d e f g h i j k l m n o p q r s t u"), ",")} {
		var out, diagnostic bytes.Buffer
		if code := Run([]string{"skill-generations", "generate", "--config", path, "--id", "attempt", "--model", "generator", "--name", "workflow", "--tasks", tasks}, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 {
			t.Fatal("invalid tasks admitted", tasks, code, diagnostic.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid command created config", err)
	}
}
