package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestRecoveredModelContinuationProcessHelper(t *testing.T) {
	if os.Getenv("DARWIN_MODEL_CONTINUATION_CHILD") != "1" {
		t.Skip("owned child only")
	}
	path, task := os.Getenv("DARWIN_MODEL_CONTINUATION_CONFIG"), os.Getenv("DARWIN_MODEL_CONTINUATION_SOURCE")
	if !filepath.IsAbs(path) || !sessions.ValidEventPageID(task) {
		os.Exit(90)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		os.Exit(91)
	}
	var cfg config.Settings
	if json.Unmarshal(body, &cfg) != nil {
		os.Exit(92)
	}
	service, err := NewService(cfg, nil)
	if err != nil {
		os.Exit(93)
	}
	service.profile = delegateProcessProfile
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	result, err := service.Run(ctx, Request{ModelID: "chat", Prompt: "explicit recovered follow-up", ContinueTaskID: task})
	if os.Getenv("DARWIN_MODEL_CONTINUATION_DENIED") == "1" {
		if !errors.Is(err, ErrAdmission) || result.TaskID != "" {
			os.Exit(94)
		}
		_, _ = io.WriteString(os.Stdout, "denied\n")
		os.Exit(0)
	}
	if err != nil || !sessions.ValidEventPageID(result.TaskID) || result.Text != "fresh continuation answer" {
		os.Exit(95)
	}
	_, _ = io.WriteString(os.Stdout, result.TaskID+"\n")
	os.Exit(0)
}

func runRecoveredModelContinuationChild(t *testing.T, ctx context.Context, configuration, task string, denied bool) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveredModelContinuationProcessHelper$", "-test.count=1")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "DARWIN_MODEL_CONTINUATION_CHILD=1", "DARWIN_MODEL_CONTINUATION_CONFIG=" + configuration, "DARWIN_MODEL_CONTINUATION_SOURCE=" + task}
	if denied {
		cmd.Env = append(cmd.Env, "DARWIN_MODEL_CONTINUATION_DENIED=1")
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("owned continuation pipe unavailable")
	}
	if cmd.Start() != nil {
		t.Fatal("owned continuation child failed to start")
	}
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	body, readErr := io.ReadAll(io.LimitReader(pipe, 4097))
	if readErr != nil || len(body) > 4096 {
		t.Fatal("owned continuation response exceeded bound")
	}
	err = cmd.Wait()
	joined = true
	if err != nil || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("fresh continuation process failed; output withheld")
	}
	line := strings.TrimSuffix(string(body), "\n")
	if denied {
		if line != "denied" {
			t.Fatal("cancellation was not denied")
		}
		return ""
	}
	if !sessions.ValidEventPageID(line) || line == task {
		t.Fatal("invalid new task identity")
	}
	return line
}
