package v1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKSummaryPreparationExactReplayAndInspection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"message":{"content":"{\"version\":1,\"summary\":{\"requirements\":[\"preserve source\"]}}"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	directory := t.TempDir()
	settings := fmt.Sprintf(`version: 1
mode: cloud_only
telemetry:
  database: %q
providers:
  - id: fixture
    kind: ollama
    endpoint: %q
models:
  - id: chat
    provider: fixture
    model: fixture
    locality: cloud
    capabilities: [chat]
    context_tokens: 16384
    estimated_cost: 0
`, filepath.Join(directory, "summary.db"), server.URL)
	path := filepath.Join(directory, "settings.yaml")
	if err := os.WriteFile(path, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{ProjectFile: path})
	if err != nil {
		t.Fatal(err)
	}
	source, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "retain this requirement"})
	if err != nil {
		t.Fatal(err)
	}
	key := "sdk-summary-preparation-key-0001"
	request := sdk.PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "chat", Keep: 1, MaxCost: 0}
	first, err := client.PrepareSummary(ctx, key, request)
	if err != nil || first.Validate() != nil || first.TerminalAttempt == nil || first.TerminalAttempt.Status != "drafted" || calls.Load() != 2 {
		t.Fatalf("first preparation=%+v calls=%d err=%v", first, calls.Load(), err)
	}
	second, err := client.PrepareSummary(ctx, key, request)
	if err != nil || !reflect.DeepEqual(first, second) || calls.Load() != 2 {
		t.Fatalf("replay redispatched: calls=%d err=%v", calls.Load(), err)
	}
	inspected, err := client.InspectSummaryPreparation(ctx, first.Start.OperationID)
	if err != nil || !reflect.DeepEqual(first, inspected) || calls.Load() != 2 {
		t.Fatalf("inspection changed preparation: calls=%d err=%v", calls.Load(), err)
	}
	body, _ := json.Marshal(first)
	if strings.Contains(string(body), key) {
		t.Fatal("raw idempotency key exposed")
	}
	if _, err = client.PrepareSummary(ctx, "short", request); err == nil || calls.Load() != 2 {
		t.Fatal("malformed key dispatched")
	}
}
