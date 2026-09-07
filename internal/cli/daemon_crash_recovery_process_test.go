//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	"go.yaml.in/yaml/v3"
)

type daemonProcessOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *daemonProcessOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *daemonProcessOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

type ownedDaemonProcess struct {
	command *exec.Cmd
	done    chan struct{}
	output  *daemonProcessOutput
	waitErr error
}

func TestDaemonRestartRecoversInterruptedModelWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := os.Getenv("DARWIN_TEST_DAEMON_BINARY")
	if binary == "" {
		binary = filepath.Join(dir, "darwin")
		if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/darwin").CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	} else if !filepath.IsAbs(binary) {
		t.Fatal("test daemon binary must be absolute")
	}

	started := make(chan struct{}, 1)
	disconnected := make(chan struct{}, 1)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
		case "/api/chat":
			if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&map[string]any{}) != nil {
				t.Error("invalid provider request")
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			_, _ = io.WriteString(w, "{\"message\":{\"content\":\"partial-not-an-answer\"},\"done\":false}\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			select {
			case disconnected <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Daemon.Listen = address
	cfg.Workers.Max = 1
	cfg.Memory.Enabled = false
	cfg.Skills.Enabled = false
	cfg.Tools.Enabled = false
	cfg.Telemetry.Database = filepath.Join(dir, "daemon-crash.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", ContextTokens: 16384, EstimatedCost: &zero, RAMBytes: 1, Capabilities: []string{"chat"}}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configuration, body, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("daemon-crash-token-", 2)
	owners := filepath.Join(dir, "process-owners")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.Transport.(*http.Transport).CloseIdleConnections()

	first := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(first)
	waitDaemonReady(t, ctx, first, client, address, token)
	submission := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/submissions", `{"model_id":"chat","prompt":"survive daemon crash"}`, "daemon-crash-submission")
	if submission.State != "queued" || submission.ID == "" {
		t.Fatal("submission was not durably queued", submission)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("daemon never reached provider stream")
	}
	running := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodGet, "/v1/submissions/"+submission.ID, "", "")
	if running.State != "running" || len(running.TaskIDs) != 1 || running.Result != nil || calls.Load() != 1 {
		t.Fatal("wrong pre-crash state", running, calls.Load())
	}
	if err = first.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-first.done
	if err = first.waitErr; err == nil || first.command.ProcessState == nil {
		t.Fatal("daemon did not terminate abruptly", err)
	}
	status, ok := first.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("daemon did not die by SIGKILL")
	}
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("provider stream remained connected after daemon death")
	}

	database, err := sql.Open("sqlite", cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	result, err := database.ExecContext(ctx, `UPDATE submissions SET lease_expires_at=? WHERE id=? AND state='running'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), submission.ID)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	changed, err := result.RowsAffected()
	closeErr := database.Close()
	if err != nil || closeErr != nil || changed != 1 {
		t.Fatal("failed to advance owned crash fixture", err, closeErr, changed)
	}

	second := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(second)
	waitDaemonReady(t, ctx, second, client, address, token)
	var recovered submissions.Status
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		recovered = daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodGet, "/v1/submissions/"+submission.ID, "", "")
		if recovered.State != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if recovered.State != "failed" || recovered.ErrorCode != "execution_failed" || recovered.Result == nil || recovered.Result.TaskID != running.TaskIDs[0] || recovered.Result.Text != "" || calls.Load() != 1 {
		t.Fatal("restart did not safely recover interrupted work", recovered, calls.Load())
	}
	recoveries := daemonRequest[[]submissions.Recovery](t, ctx, client, address, token, http.MethodGet, "/v1/submissions/"+submission.ID+"/recoveries", "", "")
	if len(recoveries) != 1 || recoveries[0].SubmissionID != submission.ID || recoveries[0].Reason != "interrupted_model" || recoveries[0].Action != "failed" {
		t.Fatal("missing restart recovery receipt", recoveries)
	}
	snapshot := daemonRequest[sessions.Snapshot](t, ctx, client, address, token, http.MethodGet, "/v1/tasks/"+running.TaskIDs[0], "", "")
	if snapshot.TaskID != running.TaskIDs[0] || snapshot.State != "failed" || !snapshot.InterruptedTurn || len(snapshot.Messages) != 1 || snapshot.Messages[0].Role != "user" || snapshot.Messages[0].Content != "survive daemon crash" || strings.Contains(snapshot.Messages[0].Content, "partial-not-an-answer") {
		t.Fatal("recovered task published partial output or lost durable context", snapshot)
	}

	control, err := daemonClient(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	live, err := control.Status(ctx)
	if err == nil {
		_, err = control.Stop(ctx, live.InstanceID)
	}
	control.Close()
	if err != nil {
		t.Fatal("graceful stop after recovery failed", err)
	}
	<-second.done
	if err = second.waitErr; err != nil {
		t.Fatal("restarted daemon did not exit cleanly", err, second.output.String())
	}
}

func startOwnedDaemon(t *testing.T, ctx context.Context, binary, configuration, token, owners string) *ownedDaemonProcess {
	t.Helper()
	command := exec.CommandContext(ctx, binary, "serve", "--config", configuration)
	command.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_API_TOKEN=" + token, "DARWIN_PROCESS_OWNER_DIR=" + owners}
	output := &daemonProcessOutput{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &ownedDaemonProcess{command: command, done: make(chan struct{}), output: output}
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	return process
}

func killAndJoinOwnedDaemon(process *ownedDaemonProcess) {
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.command.Process.Kill()
	<-process.done
}

func waitDaemonReady(t *testing.T, ctx context.Context, process *ownedDaemonProcess, client *http.Client, address, token string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatal("daemon exited before readiness", process.waitErr, process.output.String())
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err == nil {
			var report health.Report
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report)
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && closeErr == nil && report.Validate() == nil && report.Ready {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready", process.output.String())
}

func daemonRequest[T any](t *testing.T, ctx context.Context, client *http.Client, address, token, method, path, body, key string) T {
	t.Helper()
	var out T
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("daemon request failed", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out) != nil {
		t.Fatal("invalid daemon response", method, path, response.StatusCode)
	}
	return out
}
