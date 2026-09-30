package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestCompletedTaskRecoveredAfterOwnerProcessKilled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"durable completed answer"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":4}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "completed-crash.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	configuration := filepath.Join(t.TempDir(), "config.json")
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryClaimProcessHelper$")
	cmd.Env = append(os.Environ(), "DARWIN_RECOVERY_HELPER=1", "DARWIN_RECOVERY_CONFIG="+configuration, "DARWIN_RECOVERY_STAGE=terminal")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(pipe).ReadString('\n'); line <- strings.TrimSpace(text) }()
	var id string
	select {
	case id = <-line:
	case <-ctx.Done():
		t.Fatal("task completion not acknowledged")
	}
	if id == "" {
		t.Fatal("missing submission ID")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil {
		t.Fatal("worker was not killed")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.SubmissionStatus(ctx, id)
	if err != nil || before.State != "running" || len(before.TaskIDs) != 1 || calls.Load() != 1 {
		t.Fatal(before, err, calls.Load())
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	completed, err := sessions.Replay(ctx, db, before.TaskIDs[0])
	if err != nil || completed.State != "completed" {
		t.Fatal(completed, err)
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	after := awaitSubmission(t, ctx, svc, id, "succeeded")
	if after.Result == nil || after.Result.Text != "durable completed answer" || after.Result.TaskID != before.TaskIDs[0] || after.Result.AuditStatus != "not_recovered" || calls.Load() != 1 {
		t.Fatal(after, calls.Load())
	}
	if after.Result.Usage == nil || after.Result.Usage.InputTokens != 3 || after.Result.Usage.OutputTokens != 4 {
		t.Fatal(after.Result.Usage)
	}
	again, err := sessions.Replay(ctx, db, before.TaskIDs[0])
	if err != nil || again.Sequence != completed.Sequence || again.State != completed.State {
		t.Fatal("recovery rewrote execution history", again, err)
	}
	history, err := svc.SubmissionRecoveries(ctx, id)
	if err != nil || len(history) != 1 || history[0].Reason != "terminal_history" || history[0].Action != "succeeded" {
		t.Fatal(history, err)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
}
