package v1_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/ArronJablonowski/DarwinRouter/providers"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKSummaryFailedDraftRemainsInspectableAndUnapprovable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls, drafts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []providers.Message `json:"messages"`
			Tools    []providers.Tool    `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid fixture", http.StatusBadRequest)
			return
		}
		text := "Retain the original requirement."
		if len(request.Messages) == 2 && request.Messages[0].Role == "system" && strings.Contains(request.Messages[0].Content, "session-compaction draft") {
			drafts.Add(1)
			if len(request.Tools) != 0 {
				t.Error("summary requested tools")
			}
			text = "malformed auxiliary output, not JSON"
		}
		body, _ := json.Marshal(text)
		fmt.Fprintf(w, "{\"message\":{\"content\":%s},\"done\":true,\"done_reason\":\"stop\"}\n", body)
	}))
	defer server.Close()
	dir := t.TempDir()
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
`, filepath.Join(dir, "summary.db"), server.URL)
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	options := sdk.ConfigOptions{ProjectFile: path}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	source, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Retain original requirement"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.InspectTask(ctx, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.SummarizeTask(ctx, source.TaskID, "chat", 1, 0)
	if err == nil || attempt.Validate() != nil || attempt.Status != "failed" || attempt.Code != "summary_failed" || attempt.Draft != nil || calls.Load() != 2 || drafts.Load() != 1 {
		t.Fatal("invalid output did not produce one durable failed attempt", err)
	}
	reopened, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.InspectSummaryAttempt(ctx, attempt.ID)
	if err != nil || !reflect.DeepEqual(saved, attempt) {
		t.Fatal("failed attempt not durable", err)
	}
	page, err := reopened.ListSummaryAttempts(ctx, source.TaskID, "", 100)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], attempt) {
		t.Fatal("failed attempt missing or automatic retry recorded", err)
	}
	if _, err := reopened.ReviewSummary(ctx, attempt.ID, "", "approved", "Attempted approval of failed generation"); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal("failed generation was approvable", err)
	}
	if _, err := reopened.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", ContinueTaskID: source.TaskID, SummaryAttemptID: attempt.ID, Prompt: "Continue"}); err == nil {
		t.Fatal("failed generation could be used for continuation")
	}
	for _, id := range []string{attempt.ID, "unknown-attempt"} {
		history, err := reopened.SummaryReviewHistory(ctx, id)
		if err != nil || history == nil || len(history) != 0 {
			t.Fatal("absent review history must be empty", err)
		}
	}
	unknown, err := reopened.InspectSummaryAttempt(ctx, "unknown-attempt")
	if !errors.Is(err, sdk.ErrInspection) || !reflect.DeepEqual(unknown, sdk.SummaryAttempt{}) {
		t.Fatal("unknown attempt detail must fail without data", err)
	}
	after, err := reopened.InspectTask(ctx, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed summary workflow changed source", err)
	}
	if calls.Load() != 2 || drafts.Load() != 1 {
		t.Fatal("inspection, failed approval, or continuation retried inference")
	}
}
