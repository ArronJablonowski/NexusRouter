package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestDurableTaskStreamDisconnectResumeAndTerminalReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-release:
			fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", token+" answer")
			stopped <- struct{}{}
		}
	}))
	defer provider.Close()

	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	svc, err := newAPIFixtureService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(token, 8, Services{
		Run:              svc.Run,
		Submit:           svc.Submit,
		ResumeSubmission: svc.ResumeSubmission,
		SubmissionStream: db.ReadSubmissionStreamPage,
		Inspect:          func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:           func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	body, key := `{"model_id":"chat","prompt":"hello"}`, "durable-stream-key-0001"
	request := func(cursor string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/tasks/stream", strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if cursor != "" {
			r.Header.Set("Last-Event-ID", cursor)
		}
		return server.Client().Do(r)
	}

	response, err := request("")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	scanner := bufio.NewScanner(response.Body)
	var firstID string
	var firstKind runtime.Kind
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			firstID = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "event: ") {
			firstKind = runtime.Kind(strings.TrimPrefix(line, "event: "))
		}
		if line == "" && firstID != "" {
			break
		}
	}
	if firstKind != runtime.TaskStarted || !strings.HasSuffix(firstID, ":1") {
		t.Fatal("missing durable first event", firstID, firstKind, scanner.Err())
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	response.Body.Close()
	select {
	case <-stopped:
		t.Fatal("disconnect canceled detached execution")
	case <-time.After(100 * time.Millisecond):
	}
	type retryOutcome struct {
		body []byte
		code int
		err  error
	}
	const concurrentRetries = 4
	ready := make(chan struct{}, concurrentRetries)
	outcomes := make(chan retryOutcome, concurrentRetries)
	resumeReady := make(chan struct{}, 1)
	runningResume := make(chan retryOutcome, 1)
	go func() {
		response, err := request(firstID)
		if err != nil {
			runningResume <- retryOutcome{err: err}
			return
		}
		resumeReady <- struct{}{}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		runningResume <- retryOutcome{body: body, code: response.StatusCode, err: readErr}
	}()
	for i := 0; i < concurrentRetries; i++ {
		go func() {
			response, err := request("")
			if err != nil {
				outcomes <- retryOutcome{err: err}
				return
			}
			ready <- struct{}{}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			outcomes <- retryOutcome{body: body, code: response.StatusCode, err: readErr}
		}()
	}
	for i := 0; i < concurrentRetries; i++ {
		select {
		case <-ready:
		case outcome := <-outcomes:
			t.Fatal("concurrent retry failed before streaming", outcome)
		case <-ctx.Done():
			t.Fatal("concurrent retry did not attach")
		}
	}
	select {
	case <-resumeReady:
	case outcome := <-runningResume:
		t.Fatal("running resume failed before streaming", outcome)
	case <-ctx.Done():
		t.Fatal("running resume did not attach")
	}
	close(release)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("provider did not finish")
	}
	var completeReplay []byte
	for i := 0; i < concurrentRetries; i++ {
		outcome := <-outcomes
		if outcome.err != nil || outcome.code != http.StatusOK || !strings.Contains(string(outcome.body), "event: result") || strings.Contains(string(outcome.body), token) {
			t.Fatal("concurrent retry", outcome.code, string(outcome.body), outcome.err)
		}
		if completeReplay == nil {
			completeReplay = outcome.body
		} else if string(outcome.body) != string(completeReplay) {
			t.Fatal("concurrent observers did not converge on identical durable replay")
		}
	}
	completeFrames := parseStreamFrames(t, string(completeReplay))
	if len(completeFrames) < 3 || completeFrames[len(completeFrames)-1].event != "result" {
		t.Fatal("incomplete durable replay", completeFrames)
	}
	for i, frame := range completeFrames {
		want := fmt.Sprintf(":%d", i+1)
		if !strings.HasSuffix(frame.id, want) || i+1 < len(completeFrames) && frame.event == "result" {
			t.Fatal("non-contiguous or premature terminal cursor", i, frame)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent retries redispatched provider", calls.Load())
	}

	resumed := <-runningResume
	if resumed.err != nil || resumed.code != http.StatusOK || strings.Contains(string(resumed.body), token) || strings.Contains(string(resumed.body), "id: "+firstID+"\n") || !strings.Contains(string(resumed.body), "event: task.completed") || !strings.Contains(string(resumed.body), "event: result") || !strings.Contains(string(resumed.body), "[REDACTED] answer") {
		t.Fatal(string(resumed.body), resumed.err)
	}
	frames := parseStreamFrames(t, string(resumed.body))
	last := frames[len(frames)-1]
	if last.event != "result" || last.id == "" {
		t.Fatal(last)
	}

	// A fresh service and store with the provider offline simulate daemon
	// restart. Resume from the final runtime event reconstructs exactly the
	// virtual durable result marker; resume after that marker is empty.
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	provider.Close()
	reopened, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedSvc, err := newAPIFixtureService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(token, 1, Services{Run: restartedSvc.Run, Submit: restartedSvc.Submit, ResumeSubmission: restartedSvc.ResumeSubmission, SubmissionStream: reopened.ReadSubmissionStreamPage, Inspect: func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil }, Health: func(context.Context) error { return nil }})
	restartedServer := httptest.NewServer(restarted)
	defer restartedServer.Close()
	restartRequest := func(cursor string) (*http.Response, error) {
		r, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, restartedServer.URL+"/v1/tasks/stream", strings.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Last-Event-ID", cursor)
		return restartedServer.Client().Do(r)
	}
	beforeResult := completeFrames[len(completeFrames)-2].id
	terminal, err := restartRequest(beforeResult)
	if err != nil || terminal.StatusCode != http.StatusOK {
		t.Fatal(terminal, err)
	}
	terminalBody, _ := io.ReadAll(terminal.Body)
	terminal.Body.Close()
	terminalFrames := parseStreamFrames(t, string(terminalBody))
	if len(terminalFrames) != 1 || terminalFrames[0].event != "result" || terminalFrames[0].id != last.id || calls.Load() != 1 {
		t.Fatalf("restart did not reconstruct terminal marker: frames=%+v calls=%d", terminalFrames, calls.Load())
	}
	afterResult, err := restartRequest(last.id)
	if err != nil || afterResult.StatusCode != http.StatusOK {
		t.Fatal(afterResult, err)
	}
	afterResultBody, _ := io.ReadAll(afterResult.Body)
	afterResult.Body.Close()
	if len(afterResultBody) != 0 || calls.Load() != 1 {
		t.Fatalf("terminal replay duplicated delivery/execution: %q calls=%d", afterResultBody, calls.Load())
	}

	changed, err := func() (*http.Response, error) {
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/tasks/stream", strings.NewReader(`{"model_id":"chat","prompt":"changed"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		return server.Client().Do(r)
	}()
	if err != nil {
		t.Fatal(err)
	}
	changedBody, _ := io.ReadAll(changed.Body)
	changed.Body.Close()
	if changed.StatusCode != http.StatusConflict || string(changedBody) != "{\"error\":\"task_conflict\"}\n" || calls.Load() != 1 {
		t.Fatal(changed.StatusCode, string(changedBody), calls.Load())
	}
}
