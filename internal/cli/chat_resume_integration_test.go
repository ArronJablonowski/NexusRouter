package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"go.yaml.in/yaml/v3"
)

func chatResumeConfiguration(t *testing.T, endpoint string) (config.Settings, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = true
	cfg.Hardware.Concurrent = "1"
	cfg.Memory.Enabled = false
	cfg.Skills.Enabled = false
	cfg.Skills.AutoDraft = false
	cfg.Skills.AutoActivate = false
	cfg.Evaluation.Judge = false
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "chat.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: endpoint}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "fixture", Model: "fixture", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func TestChatResumeRealCLIContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	requests := make(chan []providers.Message, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid provider request")
			return
		}
		calls.Add(1)
		select {
		case requests <- request.Messages:
		case <-ctx.Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "fixture answer"}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	cfg, path := chatResumeConfiguration(t, server.URL)
	service, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.Run(ctx, app.Request{ModelID: "chat", Prompt: "original prompt"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
	case <-ctx.Done():
		t.Fatal("source request unavailable")
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, err := db.Read(ctx, source.TaskID, 0, 100)
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
	done := make(chan int, 1)
	go func() {
		done <- RunWithInput([]string{"chat", "--config", path, "--model", "chat"}, input, out, io.Discard, "fixture")
	}()
	joined := false
	defer func() {
		_ = writer.Close()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("CLI did not join")
			}
		}
	}()
	if _, err := io.WriteString(writer, "/tasks\n"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, source.TaskID+"  completed")
	if calls.Load() != 1 {
		t.Fatal("task discovery performed inference")
	}
	counts, err := db.Metrics(ctx)
	if err != nil || counts.Groups[0].Counts[0].Value != 0 || counts.Groups[0].Counts[1].Value != 1 {
		t.Fatal("task discovery created task")
	}
	if _, err := io.WriteString(writer, "/resume "+source.TaskID+"\n"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, "Saved context selected:")
	if calls.Load() != 1 {
		t.Fatal("selection performed inference")
	}
	counts, err = db.Metrics(ctx)
	if err != nil || counts.Groups[0].Counts[0].Value != 0 || counts.Groups[0].Counts[1].Value != 1 {
		t.Fatal("selection created task")
	}
	selected, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, selected) {
		t.Fatal("selection mutated source")
	}
	if _, err := io.WriteString(writer, "continue prompt\n"); err != nil {
		t.Fatal(err)
	}
	out.wait(t, "[task completed]")
	_ = writer.Close()
	select {
	case code := <-done:
		joined = true
		if code != 0 {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("CLI did not finish")
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected inference count")
	}
	var request []providers.Message
	select {
	case request = <-requests:
	case <-ctx.Done():
		t.Fatal("continuation request unavailable")
	}
	if len(request) != 3 || request[0].Content != "original prompt" || request[1].Role != "assistant" || request[1].Content != "fixture answer" || request[2].Content != "continue prompt" {
		t.Fatal("source history not imported")
	}
	out.mu.Lock()
	rendered := out.text.String()
	out.mu.Unlock()
	var task string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "[task ") && strings.HasSuffix(line, "]") && line != "[task completed]" {
			task = strings.TrimSuffix(strings.TrimPrefix(line, "[task "), "]")
		}
	}
	if task == "" || task == source.TaskID {
		t.Fatal("missing new task")
	}
	snapshot, err := sessions.Replay(ctx, db, task)
	if err != nil || snapshot.State != "completed" || snapshot.ParentTaskID != source.TaskID {
		t.Fatal("continuation lineage", err)
	}
	after, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source event history changed")
	}
}

func TestChatResumeRealCLIMissingStoreDoesNotCreate(t *testing.T) {
	cfg, path := chatResumeConfiguration(t, "http://127.0.0.1:1")
	var out, diagnostic bytes.Buffer
	code := RunWithInput([]string{"chat", "--config", path, "--model", "chat"}, strings.NewReader("/resume absent-task\n/quit\n"), &out, &diagnostic, "fixture")
	if code != 0 || !strings.Contains(out.String(), "Saved context not selected:") || strings.Contains(out.String()+diagnostic.String(), cfg.Telemetry.Database) {
		t.Fatal("invalid missing-store response")
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("resume created missing database", err)
	}
}
