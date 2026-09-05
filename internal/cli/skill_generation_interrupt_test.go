package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"go.yaml.in/yaml/v3"
)

func TestSkillGenerationInterruptHelper(t *testing.T) {
	if os.Getenv("DARWIN_TEST_SKILL_INTERRUPT") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) {
			os.Exit(Run([]string{"skill-generations", "generate", "--config", os.Args[i+1], "--id", "interrupted", "--model", "generator", "--name", "workflow", "--tasks", os.Args[i+2]}, os.Stdout, os.Stderr, "test"))
		}
	}
	os.Exit(2)
}

func TestSkillGenerationSIGINTPersistsFailureWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var calls atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 2 {
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"A completed creative response"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "cloud_only" // Fixture traffic is loopback; avoid host RAM assumptions.
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
	cfg.Skills.Enabled, cfg.Skills.AutoDraft, cfg.Skills.LocalOnly = true, true, false
	cfg.Skills.Scope = "project"
	cfg.Skills.Root = filepath.Join(t.TempDir(), "unused-catalog")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSkillGenerationInterruptHelper$", "--", path, strings.Join(tasks, ","))
	cmd.Env = append(os.Environ(), "DARWIN_TEST_SKILL_INTERRUPT=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("child exited before inference: %v %s", err, stderr.String())
	case <-ctx.Done():
		<-done
		t.Fatal("child did not dispatch before deadline")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	if err := <-done; err == nil || ctx.Err() != nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("unexpected interruption result: %v %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("interruption returned a proposal")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.SkillGenerationAttempt(ctx, "interrupted")
	if err != nil || a.Status != "failed" || a.Code != "canceled" || a.Result != nil {
		t.Fatalf("missing durable cancellation: %+v %v", a, err)
	}
	var retryOut, retryErr bytes.Buffer
	code := Run([]string{"skill-generations", "generate", "--config", path, "--id", "interrupted", "--model", "generator", "--name", "workflow", "--tasks", strings.Join(tasks, ",")}, &retryOut, &retryErr, "test")
	if code != 1 || calls.Load() != 3 || retryOut.Len() != 0 {
		t.Fatal("canceled attempt redispatched", code, calls.Load())
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("interrupted generation published a skill", err)
	}
}
