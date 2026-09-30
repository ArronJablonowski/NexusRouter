//go:build darwin

package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Real signed-in Sol inference, explicitly opted in. The local HTTP worker is
// controlled (not Ollama inference) so cancellation occurs at a known boundary.
// Process observation never signals PIDs or assumes global process-name ownership.
// macOS-only qualification: Linux comm may truncate names and needs a separate
// identity observation strategy before asserting the exact Code Mode host.
func TestLiveCodexCancellationDuringDelegation(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_CANCELLATION") != "1" {
		t.Skip("explicit supervised inference only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex unavailable")
	}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var workerCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if workerCalls.Add(1) != 1 {
			t.Error("worker repeated")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
		}
	}))
	defer func() { cancel(); close(release); local.Close() }()
	cfg := codexTaskConfig(t)
	cfg.Providers[0].Executable = bin
	cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 1
	cfg.Workers.DelegateModel = "worker"
	zero := 0.0
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "local", Kind: "ollama", Endpoint: local.URL})
	cfg.Models = append(cfg.Models, config.Model{ID: "worker", Provider: "local", Model: "controlled-worker", Locality: "local", RAMBytes: 1, ContextTokens: 4096, EstimatedCost: &zero, Capabilities: []string{"chat"}})
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 8 << 30, AvailableRAM: 7 << 30}, nil
	}
	var dir string
	svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		dir = spec.CWD
		return codexbridge.LaunchChecked(ctx, spec)
	}
	parent := make(chan string, 1)
	done, joined := make(chan error, 1), make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("task cleanup did not join")
		}
	}()
	go func() {
		defer close(joined)
		_, err := svc.Run(runCtx, Request{ModelID: "brain", Prompt: "Supervised cancellation test. Call darwin.delegate exactly once. Ask the worker for raw Go source: package answer with func Answer() int returning 42. Set validation to go_source. Do not use other tools or read files. Wait for the worker result before answering.", eventSink: func(e runtime.Event) {
			if e.Kind == runtime.TaskStarted && e.Data.ParentTaskID == "" {
				parent <- e.TaskID
			}
		}})
		done <- err
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatal("coordinator ended before controlled worker entry")
	case <-ctx.Done():
		t.Fatal("worker entry timed out")
	}
	var task string
	select {
	case task = <-parent:
	case <-ctx.Done():
		t.Fatal("parent missing")
	}
	snapshot, err := captureCodexProcesses(ctx)
	if err != nil {
		t.Fatal("process observation unavailable")
	}
	owned := map[int]codexObservedProcess{}
	leaders := 0
	for pid, p := range snapshot {
		if p.Parent == os.Getpid() && filepath.Base(p.Command) == "codex" {
			leaders++
			owned[pid] = p
			for id, child := range codexDescendants(snapshot, pid) {
				owned[id] = child
			}
		}
	}
	if leaders != 1 {
		t.Fatal("could not identify unique task-owned coordinator")
	}
	hostObserved := false
	for _, p := range owned {
		hostObserved = hostObserved || filepath.Base(p.Command) == "codex-code-mode-host"
	}
	if !hostObserved {
		t.Fatal("Code Mode host was not observed; helper cleanup is unqualified")
	}
	t.Logf("at worker entry: owned process count=%d", len(owned))
	for _, p := range owned {
		t.Logf("observed executable basename: %s", filepath.Base(p.Command))
	}
	control, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.CancelTask(ctx, task); err != nil {
		t.Fatal("durable cancellation failed")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("task did not report cancellation")
		}
	case <-ctx.Done():
		t.Fatal("task did not stop")
	}
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("worker request retained")
	}
	if workerCalls.Load() != 1 {
		t.Fatal("worker repeated")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("coordinator directory retained")
	}
	verifyCanceledCodexTree(t, ctx, cfg.Telemetry.Database, task)
	deadline, end := context.WithTimeout(ctx, 5*time.Second)
	defer end()
	for {
		current, err := captureCodexProcesses(deadline)
		if err != nil {
			t.Fatal("post-cancel process observation failed")
		}
		remaining := 0
		for pid := range owned {
			if _, ok := current[pid]; ok {
				remaining++
			}
		}
		if remaining == 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("observed task processes remain after cancellation: %d", remaining)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Log("durable cancellation, worker disconnection and observed process exit verified")
}
