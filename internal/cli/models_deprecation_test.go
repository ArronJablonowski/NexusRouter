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

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

func TestModelsDeprecationArguments(t *testing.T) {
	base := []string{"--config", "fixture", "--model", "model"}
	got, err := parseModelsDeprecationArgs(base)
	if err != nil || got.domain != "general" || got.profile != "default" || got.policy.Window != 50 || got.policy.MinSamples != 20 || got.policy.FailureThreshold != .35 {
		t.Fatal(got, err)
	}
	for _, extra := range [][]string{{"--config=again"}, {"-model=again"}, {"--window=1", "--window=2"}, {"--window=0"}, {"--window=1001"}, {"--minimum-samples=0"}, {"--minimum-samples=51"}, {"--failure-threshold=NaN"}, {"--failure-threshold=Inf"}, {"--failure-threshold=0"}, {"--failure-threshold=1.01"}, {"--failure-threshold=-.1"}, {"--domain="}, {"--profile=bad\nvalue"}, {"--unknown=private-value"}, {"private-value"}} {
		var out, diagnostic bytes.Buffer
		args := append(append([]string{"models", "deprecation"}, base...), extra...)
		if code := Run(args, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-value") {
			t.Fatal(code, diagnostic.String())
		}
	}
	for _, args := range [][]string{{"models"}, {"models", "remove"}, {"models", "deprecation", "--model=x"}, {"models", "deprecation", "--config=x"}} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, &out, &diagnostic, "test"); code != 2 {
			t.Fatal(args, code)
		}
	}
}

func writeDeprecationCLIConfig(t *testing.T, cfg config.Settings) string {
	t.Helper()
	for i := range cfg.Models {
		if cfg.Models[i].EstimatedCost == nil {
			zero := 0.0
			cfg.Models[i].EstimatedCost = &zero
		}
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(config.Options{ProjectFile: path}); err != nil {
		t.Fatal("fixture config invalid", err)
	}
	return path
}

func TestModelsDeprecationMissingDatabaseNotCreated(t *testing.T) {
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "missing-private.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	cfg.Models = []config.Model{{ID: "model", Model: "model", Provider: "local", Locality: "local", Capabilities: []string{"chat"}}}
	path := writeDeprecationCLIConfig(t, cfg)
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"models", "deprecation", "--config", path, "--model", "model"}, &out, &diagnostic, "test"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private") {
		t.Fatal(code, diagnostic.String())
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("inspection created database", err)
	}
}

func TestModelsDeprecationCLIRealEvidenceReadOnly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"private-answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "evidence.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
	cfg.Models = []config.Model{{ID: "model", Model: "fixture", Provider: "fixture", Locality: "cloud", ContextTokens: 8192, Capabilities: []string{"chat"}}}
	outcome, err := app.RunExplicit(context.Background(), cfg, app.Request{ModelID: "model", Prompt: "private-prompt", Domain: "code", Profile: "default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RecordFeedback(context.Background(), cfg.Telemetry.Database, outcome.TaskID, false, 0); err != nil {
		t.Fatal(err)
	}
	path := writeDeprecationCLIConfig(t, cfg)
	before, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"models", "deprecation", "--config", path, "--model", "model", "--domain=code", "--minimum-samples=1"}
	if code := Run(args, &out, &diagnostic, "test"); code != 0 {
		t.Fatal(code, diagnostic.String())
	}
	var report evaluation.DeprecationReport
	if json.Unmarshal(out.Bytes(), &report) != nil || !report.Candidate || !report.ApprovalRequired || report.Failures != 1 || report.ConfiguredModelID != "model" || strings.Contains(out.String(), "private-") {
		t.Fatal(out.String())
	}
	after, err := os.ReadFile(cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || calls.Load() != 1 {
		t.Fatal("report changed storage or invoked provider")
	}
	t.Setenv("DARWIN_API_TOKEN", "private-domain-token")
	out.Reset()
	diagnostic.Reset()
	args = []string{"models", "deprecation", "--config", path, "--model", "model", "--domain=private-domain-token"}
	if code := Run(args, &out, &diagnostic, "test"); code != 0 || strings.Contains(out.String(), "private-domain-token") || !strings.Contains(out.String(), "[REDACTED]") || calls.Load() != 1 {
		t.Fatal("report secret redaction or no-inference boundary failed", code, diagnostic.String())
	}
}
