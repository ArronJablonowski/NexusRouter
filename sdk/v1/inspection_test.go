package v1_test

import (
	"context"
	"encoding/json"
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

	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKInspectionRejectsWithoutCreatingStorage(t *testing.T) {
	for _, client := range []*sdk.Client{nil, {}} {
		route, err := client.InspectRouteExplanation(context.Background(), "task")
		if !errors.Is(err, sdk.ErrAdmission) || route.Version != 1 || route.TaskID != "" {
			t.Fatal(route, err)
		}
		readiness, err := client.InspectTaskContinuation(context.Background(), "task")
		if !errors.Is(err, sdk.ErrAdmission) || readiness.Version != 1 || readiness.TaskID != "" {
			t.Fatal(readiness, err)
		}
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
		route, err := client.InspectRouteExplanation(ctx, "task")
		if err == nil || route.Version != 1 || route.TaskID != "" {
			t.Fatal(route, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("route cancellation identity lost", err)
		}
		readiness, err := client.InspectTaskContinuation(ctx, "task")
		if err == nil || readiness.Version != 1 || readiness.TaskID != "" || readiness.HistoryEligible {
			t.Fatal(readiness, err)
		}
		if ctx == canceled && !errors.Is(err, context.Canceled) {
			t.Fatal("continuation cancellation identity lost", err)
		}
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
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
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
hardware:
  auto_profile: false
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
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })})
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
	readiness, err := client.InspectTaskContinuation(ctx, result.TaskID)
	if err != nil || readiness.Validate() != nil || !readiness.HistoryEligible || readiness.Reason != "completed" || readiness.Sequence != snapshot.Sequence {
		t.Fatal(readiness, err)
	}
	metadata, err := json.Marshal(readiness)
	if err != nil || strings.Contains(string(metadata), "initial prompt") || strings.Contains(string(metadata), "persisted answer") {
		t.Fatal("continuation inspection leaked conversation")
	}
	automatic, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "auto", Prompt: "automatic private prompt", Domain: "code"})
	if err != nil {
		t.Fatal(err)
	}
	route, err := client.InspectRouteExplanation(ctx, automatic.TaskID)
	if err != nil || route.Validate() != nil || route.TaskID != automatic.TaskID || route.Model != "fixture" || route.Provider != "local" || route.Usage == nil || route.Usage.Scope.TaskID != automatic.TaskID {
		t.Fatal(route, err)
	}
	routeBody, err := json.Marshal(route)
	if err != nil || strings.Contains(string(routeBody), "automatic private prompt") || strings.Contains(string(routeBody), "persisted answer") || strings.Contains(string(routeBody), server.URL) {
		t.Fatal("route inspection leaked execution content", string(routeBody), err)
	}
	route.Candidates[0].Model = "mutated"
	route.Selection.Ranked[0].Model = "mutated"
	route.Usage.Scope.TaskID = "mutated"
	freshRoute, err := client.InspectRouteExplanation(ctx, automatic.TaskID)
	if err != nil || freshRoute.Model != "fixture" || freshRoute.Candidates[0].Model != "fixture" || freshRoute.Selection.Ranked[0].Model != "fixture" || freshRoute.Usage == nil || freshRoute.Usage.Scope.TaskID != automatic.TaskID {
		t.Fatal("returned route record aliased durable state", freshRoute, err)
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
	if calls.Load() != 2 {
		t.Fatal("inspection or interrupted run dispatched provider", calls.Load())
	}
	missing, err := client.InspectTask(ctx, "unknown-task")
	if err == nil || missing.Version != 1 || missing.TaskID != "" {
		t.Fatal(missing, err)
	}
}
