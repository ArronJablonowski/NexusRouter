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

	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKSummaryWorkflowDraftReviewContinueAndRevoke(t *testing.T) {
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
			http.Error(w, "invalid fixture", 400)
			return
		}
		text := "Retain the original requirement."
		if len(request.Messages) == 2 && request.Messages[0].Role == "system" && strings.Contains(request.Messages[0].Content, "session-compaction draft") {
			drafts.Add(1)
			if len(request.Tools) != 0 || strings.Contains(request.Messages[1].Content, "sdk-summary-secret") {
				t.Error("auxiliary input violated privacy/tool contract")
			}
			text = `{"version":1,"summary":{"requirements":["Retain original requirement without sdk-summary-secret"],"pending_work":["Continue the implementation"]}}`
		} else if len(request.Messages) > 2 {
			if request.Messages[0].Role != "system" || !strings.Contains(request.Messages[1].Content, "session_summary") {
				t.Error("approved summary missing from continuation")
			}
			text = "Continued with the approved summary."
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
	options := sdk.ConfigOptions{ProjectFile: path, LookupSecret: func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "sdk-summary-secret"
		}
		return ""
	}}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	source, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Retain original requirement and sdk-summary-secret"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.InspectTask(ctx, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := client.SummarizeTask(ctx, source.TaskID, "chat", 1, 0)
	if err != nil || a.Validate() != nil || a.Status != "drafted" || a.Draft == nil || drafts.Load() != 1 || calls.Load() != 2 {
		t.Fatal("summary not drafted exactly once", err)
	}
	encoded, _ := json.Marshal(a)
	if strings.Contains(string(encoded), "sdk-summary-secret") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("summary credential redaction missing")
	}
	// A new host sees the same durable proposal without generating another one.
	reopened, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.InspectSummaryAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) || calls.Load() != 2 {
		t.Fatal("reopened inspection changed or dispatched the draft", err)
	}
	// Returned nested slices belong to the caller, not a shared stored proposal.
	saved.Draft.Request.Summary.Requirements[0] = "caller mutation"
	saved, err = reopened.InspectSummaryAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(saved, a) {
		t.Fatal("inspection exposed shared mutable state", err)
	}
	reviews, err := reopened.SummaryReviewHistory(ctx, a.ID)
	if err != nil || reviews == nil || len(reviews) != 0 || calls.Load() != 2 {
		t.Fatal("draft approved itself or review lookup dispatched", err)
	}
	req := sdk.Request{Version: 1, ModelID: "chat", ContinueTaskID: source.TaskID, SummaryAttemptID: a.ID, Prompt: "Continue"}
	if _, err := reopened.Run(ctx, req); err == nil || calls.Load() != 2 {
		t.Fatal("unapproved draft used")
	}
	approval, err := reopened.ReviewSummary(ctx, a.ID, "", "approved", "Operator fixture compared sdk-summary-secret source and draft")
	if err != nil || approval.Validate() != nil || strings.Contains(approval.Note, "sdk-summary-secret") || calls.Load() != 2 {
		t.Fatal("operator review failed or dispatched", err)
	}
	continued, err := reopened.Run(ctx, req)
	if err != nil || calls.Load() != 3 {
		t.Fatal("approved continuation failed", err)
	}
	snapshot, err := reopened.InspectTask(ctx, continued.TaskID)
	if err != nil || snapshot.Compaction == nil || snapshot.Compaction.SourceTaskID != source.TaskID || snapshot.Compaction.SummaryAttemptID != a.ID || snapshot.Compaction.SummaryReviewID != approval.ID {
		t.Fatal("continuation lost approval/source provenance", err)
	}
	if _, err := reopened.ReviewSummary(ctx, a.ID, "wrong-review", "rejected", "Stale fixture decision"); err == nil {
		t.Fatal("stale review changed decision")
	}
	rejection, err := reopened.ReviewSummary(ctx, a.ID, approval.ID, "rejected", "Operator fixture requests correction")
	if err != nil || rejection.PreviousID != approval.ID || calls.Load() != 3 {
		t.Fatal("review revision failed", err)
	}
	if _, err := reopened.Run(ctx, req); err == nil || calls.Load() != 3 {
		t.Fatal("rejected draft dispatched")
	}
	reviews, err = reopened.SummaryReviewHistory(ctx, a.ID)
	if err != nil || len(reviews) != 2 || reviews[0].ID != approval.ID || reviews[1].ID != rejection.ID {
		t.Fatal("review history not durable", err)
	}
	second, err := reopened.SummarizeTask(ctx, source.TaskID, "chat", 1, 0)
	if err != nil || second.ID == a.ID || drafts.Load() != 2 || calls.Load() != 4 {
		t.Fatal("explicit second request not a separate draft", err)
	}
	firstPage, err := reopened.ListSummaryAttempts(ctx, source.TaskID, "", 1)
	if err != nil || len(firstPage) != 1 {
		t.Fatal("first page failed", err)
	}
	lastPage, err := reopened.ListSummaryAttempts(ctx, "", firstPage[0].ID, 1)
	if err != nil || len(lastPage) != 1 || lastPage[0].ID <= firstPage[0].ID {
		t.Fatal("exclusive cursor failed", err)
	}
	empty, err := reopened.ListSummaryAttempts(ctx, source.TaskID, lastPage[0].ID, 1)
	if err != nil || empty == nil || len(empty) != 0 || calls.Load() != 4 {
		t.Fatal("empty page not read-only", err)
	}
	identities := map[string]bool{firstPage[0].ID: true, lastPage[0].ID: true}
	if !identities[a.ID] || !identities[second.ID] {
		t.Fatal("pagination lost draft")
	}
	after, err := reopened.InspectTask(ctx, source.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("summary workflow changed source", err)
	}
}
