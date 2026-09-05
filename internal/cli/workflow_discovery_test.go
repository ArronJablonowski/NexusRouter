package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"go.yaml.in/yaml/v3"
)

func TestWorkflowDiscoveryCLIRealSourceReadOnly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"private-source-answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "model", Provider: "fixture", Model: "fixture", Locality: "cloud", ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
	result, err := app.RunExplicit(context.Background(), cfg, app.Request{ModelID: "model", Prompt: "private-source-prompt", Domain: "creative"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RecordFeedback(context.Background(), cfg.Telemetry.Database, result.TaskID, true, 0); err != nil {
		t.Fatal(err)
	}
	cfg.Skills.Enabled = true
	cfg.Skills.AutoDraft = true
	cfg.Skills.Scope = "project"
	cfg.Skills.Root = filepath.Join(t.TempDir(), "uncreated")
	path := filepath.Join(t.TempDir(), "discovery.yaml")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"discover", "--config", path, "--domain=creative", "--scan-limit=1"}
	var out, diagnostic bytes.Buffer
	if code := Run(append([]string{"skill-generations"}, base...), &out, &diagnostic, "test"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var page skills.WorkflowCandidatePage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Validate("", 1) != nil || len(page.Candidates) != 1 || page.Candidates[0].TaskID != result.TaskID || page.Next != result.TaskID {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "private-source") {
		t.Fatal("discovery leaked transcript")
	}
	out.Reset()
	diagnostic.Reset()
	if code := runWorkflowDiscovery(append(append([]string(nil), base...), "--after", page.Next), &out, &diagnostic); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var empty skills.WorkflowCandidatePage
	if json.Unmarshal(out.Bytes(), &empty) != nil || len(empty.Candidates) != 0 || empty.Candidates == nil || empty.Next != "" {
		t.Fatal(out.String())
	}
	for _, panics := range []bool{false, true} {
		if code := runWorkflowDiscovery(base, skillGenerationFailingOutput{panic: panics}, &diagnostic); code != 1 {
			t.Fatal("lost output acknowledged", code)
		}
	}
	after, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("discovery changed database", err)
	}
	if calls.Load() != 1 {
		t.Fatal("discovery invoked inference", calls.Load())
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("discovery created skill root", err)
	}
}

func TestWorkflowDiscoveryCLIRejectsFlagsAndSanitizesErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-config.yaml")
	base := []string{"discover", "--config", path, "--domain", "creative"}
	for _, extra := range [][]string{{"--config", path}, {"--domain=creative"}, {"--unknown", "private-secret"}, {"--scope", "project"}, {"--after", ""}, {"--after", strings.Repeat("a", 129)}, {"--scan-limit", "0"}, {"--scan-limit", "21"}, {"--scan-limit", "01"}, {"--scan-limit", "+1"}, {"--scan-limit", "1.0"}, {"--scan-limit", "1", "--scan-limit=1"}, {"positional"}, {"--domain"}} {
		var out, diagnostic bytes.Buffer
		if code := runWorkflowDiscovery(append(append([]string(nil), base...), extra...), &out, &diagnostic); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), path) || strings.Contains(diagnostic.String(), "private-secret") {
			t.Fatal(extra, code, diagnostic.String())
		}
	}
	for _, domain := range []string{"", "../private-secret", strings.Repeat("a", 65)} {
		var out, diagnostic bytes.Buffer
		if code := runWorkflowDiscovery([]string{"discover", "--config", path, "--domain", domain}, &out, &diagnostic); code != 2 {
			t.Fatal(domain, code)
		}
	}
	var out, diagnostic bytes.Buffer
	if code := runWorkflowDiscovery(base, &out, &diagnostic); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), path) {
		t.Fatal(code, diagnostic.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid command created config", err)
	}
}
