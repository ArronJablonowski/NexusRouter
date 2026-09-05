package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestChatIORealServicePersistsContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	requests := make(chan []providers.Message, 2)
	var calls atomic.Int32
	const answer = "first \x1b[31manswer\x1b[0m\x1b]0;hidden-terminal-title\a"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		select {
		case requests <- request.Messages:
		case <-ctx.Done():
			return
		}
		content := answer
		if calls.Add(1) == 2 {
			content = "second answer"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}, "done": true, "done_reason": "stop"})
	}))
	defer provider.Close()
	defer cancel()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "chat.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1}}
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	out := &chatTestOutput{writes: make(chan string, 100)}
	tasks := make(chan string, 2)
	hooks := chatHooks{Run: func(ctx context.Context, r app.Request, emit func(runtime.Event) error) (app.Result, error) {
		return svc.RunStream(ctx, r, func(e runtime.Event) error {
			if e.Kind == runtime.TaskStarted {
				tasks <- e.TaskID
			}
			return emit(e)
		})
	}, Steer: svc.SteerTask}
	done := make(chan int, 1)
	go func() { done <- runChatIO(ctx, app.Request{ModelID: "chat"}, hooks, input, out, nil) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("chat wrapper did not join")
		}
	}()
	if _, err = io.WriteString(writer, "first prompt\n"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, "first answer")
	if _, err = io.WriteString(writer, "second prompt\n"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, "second answer")
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
		done <- code
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected provider calls", calls.Load())
	}
	first, second := <-requests, <-requests
	if len(first) != 1 || first[0].Content != "first prompt" {
		t.Fatal(first)
	}
	if len(second) != 3 || second[0].Content != "first prompt" || second[1].Role != "assistant" || second[1].Content != answer || second[2].Content != "second prompt" {
		t.Fatal("continuation not delivered", second)
	}
	firstTask, secondTask := <-tasks, <-tasks
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := sessions.Replay(ctx, db, secondTask)
	if err != nil || snapshot.State != "completed" || snapshot.ParentTaskID != firstTask || len(snapshot.Messages) != 4 {
		t.Fatal(snapshot, err)
	}
	if snapshot.Messages[1].Content != answer || snapshot.Messages[2].Content != "second prompt" {
		t.Fatal("persisted continuation lost", snapshot.Messages)
	}
	out.mu.Lock()
	rendered := out.text.String()
	out.mu.Unlock()
	if strings.Contains(rendered, "\x1b") || strings.Contains(rendered, "hidden-terminal-title") || !strings.Contains(rendered, "first answer") {
		t.Fatal("unsafe terminal output", rendered)
	}
	if _, err = input.Stat(); err != nil {
		t.Fatal("caller input closed", err)
	}
}
