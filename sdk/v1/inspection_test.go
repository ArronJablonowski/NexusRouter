package v1_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKInspectionRejectsWithoutCreatingStorage(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		snapshot, err := client.InspectTask(context.Background(), "task")
		if !errors.Is(err, sdk.ErrAdmission) || snapshot.Version != 1 || snapshot.TaskID != "" {
			t.Fatal(snapshot, err)
		}
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled, context.Background()} {
		snapshot, err := client.InspectTask(ctx, "task")
		if err == nil || snapshot.Version != 1 || snapshot.TaskID != "" || len(snapshot.Messages) != 0 {
			t.Fatal(snapshot, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity lost", err)
		}
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created storage", err)
	}
}

func TestSDKInspectionSnapshotsDoNotExecuteOrMutate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"persisted answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	defer cancel()
	dir := t.TempDir()
	body := fmt.Sprintf(`version: 1
mode: local_only
telemetry:
  database: %q
providers:
  - id: local
    kind: ollama
    endpoint: %q
models:
  - id: chat
    provider: local
    model: fixture
    locality: local
    capabilities: [chat]
    context_tokens: 8192
    estimated_cost: 0
    ram_bytes: 1
`, filepath.Join(dir, "tasks.db"), server.URL)
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "initial prompt"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.InspectTask(ctx, result.TaskID)
	if err != nil || snapshot.Version != 1 || snapshot.State != "completed" || snapshot.TaskID != result.TaskID || len(snapshot.Messages) != 2 {
		t.Fatal(snapshot, err)
	}
	if snapshot.Messages[0].Content != "initial prompt" || snapshot.Messages[1].Content != "persisted answer" {
		t.Fatal(snapshot.Messages)
	}
	snapshot.Messages[0].Content = "mutated prompt"
	snapshot.Messages[1].Content = "mutated answer"
	snapshot.MessageSequences[0] = 999
	fresh, err := client.InspectTask(ctx, result.TaskID)
	if err != nil || fresh.Messages[0].Content != "initial prompt" || fresh.Messages[1].Content != "persisted answer" || fresh.MessageSequences[0] == 999 {
		t.Fatal(fresh, err)
	}
	failed, err := client.RunStream(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "never dispatched"}, func(runtime.Event) error { return errors.New("private callback error") })
	if err == nil || failed.TaskID == "" || strings.Contains(err.Error(), "private callback error") {
		t.Fatal(failed, err)
	}
	interrupted, err := client.InspectTask(ctx, failed.TaskID)
	if err != nil || interrupted.Version != 1 || interrupted.State != "canceled" || interrupted.TaskID != failed.TaskID || len(interrupted.Messages) != 1 {
		t.Fatal(interrupted, err)
	}
	if calls.Load() != 1 {
		t.Fatal("inspection or interrupted run dispatched provider", calls.Load())
	}
	missing, err := client.InspectTask(ctx, "unknown-task")
	if err == nil || missing.Version != 1 || missing.TaskID != "" {
		t.Fatal(missing, err)
	}
}
