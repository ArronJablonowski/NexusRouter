package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"go.yaml.in/yaml/v3"
)

func TestDaemonLearningAdvancesAndResumesAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "darwin")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/nexus").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		calls.Add(1)
		http.Error(w, "unexpected inference with no workflows", http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.Defaults()
	cfg.Daemon.Listen = address
	cfg.Telemetry.Database = filepath.Join(dir, "learning.db")
	cfg.Skills.Root, cfg.Skills.Scope = cliSkillPath(t), "project"
	cfg.Skills.GenerationBudget.Enabled = true
	cfg.Skills.Learning.Enabled, cfg.Skills.Learning.ModelID, cfg.Skills.Learning.Interval = true, "generator", "1s"
	zero := 0.0
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "generator", Provider: "local", Model: "fixture", Locality: "local", ContextTokens: 4096, EstimatedCost: &zero, RAMBytes: 1, Capabilities: []string{"general"}}}
	body, _ := yaml.Marshal(cfg)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("learning-process-token-", 2)
	start := func() func() {
		cmd := exec.CommandContext(ctx, binary, "serve", "--config", path)
		cmd.Env = append(os.Environ(), "DARWIN_API_TOKEN="+token, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(dir, "owners"))
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cleanup, release := context.WithTimeout(context.Background(), 5*time.Second)
			defer release()
			client, err := daemonClient(cfg, token)
			if err == nil {
				status, queryErr := client.Status(cleanup)
				if queryErr == nil {
					_, _ = client.Stop(cleanup, status.InstanceID)
				}
				client.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Error("learning daemon exit", err, output.String())
				}
			case <-cleanup.Done():
				_ = cmd.Process.Kill() // Only this test's still-owned child.
				<-done
				t.Error("learning daemon did not join on stop")
			}
		}
		t.Cleanup(stop)
		return stop
	}
	waitRevision := func(minimum int64) int64 {
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err == nil {
				state, readErr := db.LearningState(ctx, cfg.Skills.Scope, cfg.Skills.Learning.Name)
				db.Close()
				if readErr == nil && state.Revision >= minimum {
					return state.Revision
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("daemon learning cursor did not advance")
		return 0
	}
	stopFirst := start()
	revision := waitRevision(3)
	stopFirst()
	stopSecond := start()
	waitRevision(revision + 2)
	stopSecond()
	if calls.Load() != 0 {
		t.Fatal("empty discovery invoked inference", calls.Load())
	}
	if _, err := os.Stat(cfg.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("empty discovery created a skill catalog", err)
	}
}
