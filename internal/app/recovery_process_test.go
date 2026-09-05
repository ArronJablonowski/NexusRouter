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

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
)

func TestRecoveryClaimProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_RECOVERY_HELPER") != "1" {
		return
	}
	body, err := os.ReadFile(os.Getenv("DARWIN_RECOVERY_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid fixture config")
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.Submit(context.Background(), "killed-worker-request", Request{ModelID: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ttl := time.Nanosecond
	terminal := os.Getenv("DARWIN_RECOVERY_STAGE") == "terminal"
	if terminal {
		ttl = 2 * time.Second
	}
	claim, err := db.ClaimSubmission(context.Background(), status.ConfigDigest, time.Now(), ttl)
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		// Execute and commit task completion, deliberately omitting the
		// submission-finalization acknowledgement before the parent kills us.
		if _, err := svc.Run(context.Background(), Request{ModelID: "chat", Prompt: "hello", submissionID: status.ID, submissionToken: claim.Token}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println(status.ID) // Parent kills this owner only after the committed claim.
	time.Sleep(time.Minute)
	t.Fatal("parent did not terminate fixture worker")
}

func TestDispatcherRecoversClaimAfterOwnerProcessKilled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"recovered answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "crashed.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	configuration := filepath.Join(t.TempDir(), "config.json")
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryClaimProcessHelper$")
	cmd.Env = append(os.Environ(), "DARWIN_RECOVERY_HELPER=1", "DARWIN_RECOVERY_CONFIG="+configuration, "DARWIN_RECOVERY_STAGE=claim")
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
		t.Fatal("claim not acknowledged")
	}
	if id == "" {
		t.Fatal("missing durable claim ID")
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
	if err != nil || before.State != "running" || !before.LeaseExpired || len(before.TaskIDs) != 0 || calls.Load() != 0 {
		t.Fatal(before, err, calls.Load())
	}
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	after := awaitSubmission(t, ctx, svc, id, "succeeded")
	if after.Result == nil || after.Result.Text != "recovered answer" || len(after.TaskIDs) != 1 || calls.Load() != 1 {
		t.Fatal(after, calls.Load())
	}
	history, err := svc.SubmissionRecoveries(ctx, id)
	if err != nil || len(history) != 1 || history[0].Action != "queued" {
		t.Fatal(history, err)
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
}
