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
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
	"go.yaml.in/yaml/v3"
)

type daemonQualificationProvider struct {
	mu           sync.Mutex
	calls        map[string]int
	interrupted  chan struct{}
	disconnected chan struct{}
}

func (p *daemonQualificationProvider) count(prompt string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[prompt]
}

func (p *daemonQualificationProvider) serveHTTP(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	switch r.URL.Path {
	case "/api/tags":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
	case "/api/chat":
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || len(body.Messages) == 0 {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		prompt := body.Messages[len(body.Messages)-1].Content
		p.mu.Lock()
		p.calls[prompt]++
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		if prompt == "resume source" {
			select {
			case p.interrupted <- struct{}{}:
			default:
			}
			_, _ = io.WriteString(w, `{"message":{"content":"discarded partial"},"done":false}`+"\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			select {
			case p.disconnected <- struct{}{}:
			default:
			}
			return
		}
		answer := "answer for " + prompt
		encoded, _ := json.Marshal(map[string]any{"message": map[string]string{"role": "assistant", "content": answer}, "done": true, "done_reason": "stop", "prompt_eval_count": 1, "eval_count": 1})
		_, _ = w.Write(append(encoded, '\n'))
	default:
		http.NotFound(w, r)
	}
}

func TestDaemonBranchAndRecoveredResumeAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	fixture := &daemonQualificationProvider{calls: map[string]int{}, interrupted: make(chan struct{}, 1), disconnected: make(chan struct{}, 1)}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serveHTTP(t, w, r) }))
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
	cfg.Evaluation.AutoReviewModel = ""
	cfg.Telemetry.Database = filepath.Join(dir, "qualification.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", ContextTokens: 16384, EstimatedCost: &zero, RAMBytes: 1, Capabilities: []string{"chat"}}}
	configurationBody, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configuration, configurationBody, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("branch-resume-token-", 2)
	owners := filepath.Join(dir, "owners")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.Transport.(*http.Transport).CloseIdleConnections()

	first := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(first)
	waitDaemonReady(t, ctx, first, client, address, token)
	completed := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/submissions", `{"model_id":"chat","prompt":"branch source"}`, "qualification-completed-source")
	completed = awaitDaemonSubmission(t, ctx, client, address, token, completed.ID, "succeeded")
	if completed.Result == nil || completed.Result.TaskID == "" || fixture.count("branch source") != 1 {
		t.Fatal("completed source did not execute exactly once", completed, fixture.count("branch source"))
	}
	interrupted := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/submissions", `{"model_id":"chat","prompt":"resume source"}`, "qualification-interrupted-source")
	select {
	case <-fixture.interrupted:
	case <-ctx.Done():
		t.Fatal("interrupted source did not reach provider")
	}
	interrupted = daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodGet, "/v1/submissions/"+interrupted.ID, "", "")
	if interrupted.State != "running" || len(interrupted.TaskIDs) != 1 || fixture.count("resume source") != 1 {
		t.Fatal("wrong interrupted source state", interrupted, fixture.count("resume source"))
	}
	if err = first.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-first.done
	if first.waitErr == nil || first.command.ProcessState == nil {
		t.Fatal("daemon did not terminate abruptly")
	}
	waitStatus, ok := first.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatal("daemon did not die by SIGKILL")
	}
	select {
	case <-fixture.disconnected:
	case <-ctx.Done():
		t.Fatal("provider stream remained connected after daemon death")
	}
	expireSubmissionLease(t, ctx, cfg.Telemetry.Database, interrupted.ID)

	second := startOwnedDaemon(t, ctx, binary, configuration, token, owners)
	defer killAndJoinOwnedDaemon(second)
	waitDaemonReady(t, ctx, second, client, address, token)
	recovered := awaitDaemonSubmission(t, ctx, client, address, token, interrupted.ID, "failed")
	if recovered.ErrorCode != "execution_failed" || recovered.Result == nil || recovered.Result.TaskID != interrupted.TaskIDs[0] || fixture.count("resume source") != 1 {
		t.Fatal("restart did not recover without redispatch", recovered, fixture.count("resume source"))
	}
	branchSource := daemonRequest[sessions.Snapshot](t, ctx, client, address, token, http.MethodGet, "/v1/tasks/"+completed.Result.TaskID, "", "")
	resumeSource := daemonRequest[sessions.Snapshot](t, ctx, client, address, token, http.MethodGet, "/v1/tasks/"+recovered.Result.TaskID, "", "")
	branchFence := daemonTaskFence(t, ctx, client, address, token, branchSource.SessionID, branchSource.TaskID)
	resumeFence := daemonTaskFence(t, ctx, client, address, token, resumeSource.SessionID, resumeSource.TaskID)
	branchBefore := daemonTaskEventBodies(t, ctx, cfg.Telemetry.Database, branchSource.TaskID)
	resumeBefore := daemonTaskEventBodies(t, ctx, cfg.Telemetry.Database, resumeSource.TaskID)

	branchBody := daemonDerivedSubmissionBody(t, branchFence, "branch child")
	resumeBody := daemonDerivedSubmissionBody(t, resumeFence, "resume child")
	submissionsBeforeDenial := daemonSubmissionCount(t, ctx, cfg.Telemetry.Database)
	assertDaemonDenied(t, ctx, client, address, "", "", "/v1/tasks/"+branchFence.TaskID+"/branches", branchBody, "denied-branch-auth", http.StatusUnauthorized)
	assertDaemonDenied(t, ctx, client, address, token, "https://attacker.invalid", "/v1/tasks/"+branchFence.TaskID+"/branches", branchBody, "denied-branch-origin", http.StatusForbidden)
	assertDaemonDenied(t, ctx, client, address, "", "", "/v1/tasks/"+resumeFence.TaskID+"/resumes", resumeBody, "denied-resume-auth", http.StatusUnauthorized)
	assertDaemonDenied(t, ctx, client, address, token, "https://attacker.invalid", "/v1/tasks/"+resumeFence.TaskID+"/resumes", resumeBody, "denied-resume-origin", http.StatusForbidden)
	if fixture.count("branch child") != 0 || fixture.count("resume child") != 0 || daemonSubmissionCount(t, ctx, cfg.Telemetry.Database) != submissionsBeforeDenial {
		t.Fatal("denied requests reached durable intake or provider")
	}

	branch := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/tasks/"+branchFence.TaskID+"/branches", branchBody, "qualification-branch-child")
	branchAgain := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/tasks/"+branchFence.TaskID+"/branches", branchBody, "qualification-branch-child")
	if branch.ID == "" || branchAgain.ID != branch.ID {
		t.Fatal("branch idempotency was not stable", branch, branchAgain)
	}
	branch = awaitDaemonSubmission(t, ctx, client, address, token, branch.ID, "succeeded")
	resume := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/tasks/"+resumeFence.TaskID+"/resumes", resumeBody, "qualification-resume-child")
	resumeAgain := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodPost, "/v1/tasks/"+resumeFence.TaskID+"/resumes", resumeBody, "qualification-resume-child")
	if resume.ID == "" || resumeAgain.ID != resume.ID {
		t.Fatal("resume idempotency was not stable", resume, resumeAgain)
	}
	resume = awaitDaemonSubmission(t, ctx, client, address, token, resume.ID, "succeeded")
	if branch.Result == nil || resume.Result == nil || fixture.count("branch child") != 1 || fixture.count("resume child") != 1 {
		t.Fatal("derived tasks did not execute exactly once", branch, resume, fixture.count("branch child"), fixture.count("resume child"))
	}
	branchChild := daemonRequest[sessions.Snapshot](t, ctx, client, address, token, http.MethodGet, "/v1/tasks/"+branch.Result.TaskID, "", "")
	resumeChild := daemonRequest[sessions.Snapshot](t, ctx, client, address, token, http.MethodGet, "/v1/tasks/"+resume.Result.TaskID, "", "")
	if branchChild.ParentTaskID != branchSource.TaskID || branchChild.SessionID != branchSource.SessionID || resumeChild.ParentTaskID != resumeSource.TaskID || resumeChild.SessionID != resumeSource.SessionID {
		t.Fatal("derived task lineage changed", branchChild, resumeChild)
	}
	if !equalEventBodies(branchBefore, daemonTaskEventBodies(t, ctx, cfg.Telemetry.Database, branchSource.TaskID)) || !equalEventBodies(resumeBefore, daemonTaskEventBodies(t, ctx, cfg.Telemetry.Database, resumeSource.TaskID)) {
		t.Fatal("derived submission mutated source history")
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
		t.Fatal("graceful stop failed", err)
	}
	<-second.done
	if second.waitErr != nil || strings.Contains(first.output.String(), token) || strings.Contains(second.output.String(), token) || strings.Contains(first.output.String(), "discarded partial") || strings.Contains(second.output.String(), "discarded partial") {
		t.Fatal("daemon exit or redaction failure", second.waitErr, first.output.String(), second.output.String())
	}
}

func awaitDaemonSubmission(t *testing.T, ctx context.Context, client *http.Client, address, token, id, state string) submissions.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status := daemonRequest[submissions.Status](t, ctx, client, address, token, http.MethodGet, "/v1/submissions/"+id, "", "")
		if status.State == state {
			return status
		}
		if status.State != "queued" && status.State != "running" {
			t.Fatal("submission reached unexpected terminal state", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("submission did not reach state", id, state)
	return submissions.Status{}
}

func expireSubmissionLease(t *testing.T, ctx context.Context, databasePath, id string) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := database.ExecContext(ctx, `UPDATE submissions SET lease_expires_at=? WHERE id=? AND state='running'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), id)
	var changed int64
	var rowsErr error
	if err == nil {
		changed, rowsErr = result.RowsAffected()
	}
	closeErr := database.Close()
	if err != nil || rowsErr != nil || closeErr != nil || changed != 1 {
		t.Fatal("failed to expire submission lease", err, rowsErr, closeErr, changed)
	}
}

func daemonSubmissionCount(t *testing.T, ctx context.Context, databasePath string) int {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	queryErr := database.QueryRowContext(ctx, `SELECT count(*) FROM submissions`).Scan(&count)
	closeErr := database.Close()
	if queryErr != nil || closeErr != nil {
		t.Fatal("failed to count submissions", queryErr, closeErr)
	}
	return count
}

func daemonTaskFence(t *testing.T, ctx context.Context, client *http.Client, address, token, session, task string) sessions.TaskHeadFence {
	t.Helper()
	page := daemonRequest[sessions.SessionTaskPage](t, ctx, client, address, token, http.MethodGet, "/v1/sessions/"+session+"/tasks?limit=100", "", "")
	for _, item := range page.Items {
		if item.TaskID == task {
			return item.Fence
		}
	}
	t.Fatal("task fence not found", session, task)
	return sessions.TaskHeadFence{}
}

func daemonDerivedSubmissionBody(t *testing.T, source sessions.TaskHeadFence, prompt string) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Version int                    `json:"version"`
		Source  sessions.TaskHeadFence `json:"source"`
		Request struct {
			ModelID string `json:"model_id"`
			Prompt  string `json:"prompt"`
		} `json:"request"`
	}{Version: 1, Source: source, Request: struct {
		ModelID string `json:"model_id"`
		Prompt  string `json:"prompt"`
	}{ModelID: "chat", Prompt: prompt}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertDaemonDenied(t *testing.T, ctx context.Context, client *http.Client, address, token, origin, path, body, key string, want int) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != want || bytes.Contains(responseBody, []byte("branch child")) || bytes.Contains(responseBody, []byte("resume child")) {
		t.Fatal("unexpected denial response", response.StatusCode, string(responseBody), readErr, closeErr)
	}
}

func daemonTaskEventBodies(t *testing.T, ctx context.Context, databasePath, task string) [][]byte {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.QueryContext(ctx, `SELECT body FROM events WHERE task_id=? ORDER BY sequence`, task)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	var bodies [][]byte
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			database.Close()
			t.Fatal(err)
		}
		bodies = append(bodies, append([]byte(nil), body...))
	}
	rowsErr := rows.Err()
	closeRowsErr := rows.Close()
	closeErr := database.Close()
	if rowsErr != nil || closeRowsErr != nil || closeErr != nil || len(bodies) == 0 {
		t.Fatal("failed to read event bodies", rowsErr, closeRowsErr, closeErr, len(bodies))
	}
	return bodies
}

func equalEventBodies(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
