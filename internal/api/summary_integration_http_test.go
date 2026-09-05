package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestHTTPSummaryGenerateReviewAndContinuation(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("unexpected provider request")
			http.Error(w, "invalid", 400)
			return
		}
		calls.Add(1)
		text := "source answer"
		if input.Model == "summarizer" {
			text = `{"version":1,"summary":{"requirements":["Preserve operator requirement"]}}`
		} else if len(input.Messages) > 1 {
			encoded, _ := json.Marshal(input.Messages)
			if !strings.Contains(string(encoded), "Preserve operator requirement") || strings.Contains(string(encoded), "original detail removed") {
				t.Error("continued provider context did not use approved summary")
			}
			text = "continued answer"
		}
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", text)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	// Cloud designation keeps the fixture independent of physical RAM sensors.
	cfg.Mode = "cloud_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "summary-http.db")
	cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: provider.URL}}
	zero := 0.0
	for _, id := range []string{"chat", "summarizer"} {
		cfg.Models = append(cfg.Models, config.Model{ID: id, Provider: "fixture", Model: id, Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 32768, EstimatedCost: &zero})
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := app.NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 1, Services{
		Run: svc.Run, Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) }, Health: func(context.Context) error { return nil },
		Summarize: svc.SummarizeTask, SummaryAttempt: db.SummaryAttempt, SummaryAttempts: db.ListSummaryAttempts, ReviewSummary: svc.ReviewSummary, SummaryReviews: db.SummaryReviews,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	call := func(method, path string, payload any, status int, out any) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = strings.NewReader(string(encoded))
		}
		r, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		encoded, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s status=%d want=%d body=%s error=%v", method, path, response.StatusCode, status, encoded, err)
		}
		if out != nil {
			if err := json.Unmarshal(encoded, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var source struct {
		ID string `json:"task_id"`
	}
	call("POST", "/v1/tasks", map[string]any{"model_id": "chat", "prompt": "original detail removed"}, 201, &source)
	before, err := sessions.Replay(ctx, db, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := db.Read(ctx, source.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var attempt sessions.SummaryAttempt
	call("POST", "/v1/summaries", map[string]any{"task_id": source.ID, "model_id": "summarizer", "keep": 1, "max_cost": 0}, 201, &attempt)
	if attempt.Status != "drafted" || attempt.Draft == nil || attempt.Validate() != nil {
		t.Fatalf("invalid draft: %+v", attempt)
	}
	var shown sessions.SummaryAttempt
	call("GET", "/v1/summaries/"+attempt.ID, nil, 200, &shown)
	if !reflect.DeepEqual(attempt, shown) {
		t.Fatal("GET differs from generated draft")
	}
	for _, query := range []map[string]any{{}, {"task_id": source.ID, "limit": 1}} {
		var listed []sessions.SummaryAttempt
		call("POST", "/v1/summaries/query", query, 200, &listed)
		if len(listed) != 1 || listed[0].ID != attempt.ID {
			t.Fatalf("unexpected query: %+v", listed)
		}
	}
	var approval sessions.SummaryReview
	call("POST", "/v1/summaries/reviews", map[string]any{"attempt_id": attempt.ID, "decision": "approved", "note": "Compared requirements to source"}, 201, &approval)
	var reviews []sessions.SummaryReview
	call("GET", "/v1/summaries/"+attempt.ID+"/reviews", nil, 200, &reviews)
	if len(reviews) != 1 || !reflect.DeepEqual(reviews[0], approval) {
		t.Fatal("approval history mismatch")
	}
	var continued struct {
		ID string `json:"task_id"`
	}
	continueBody := map[string]any{"model_id": "chat", "prompt": "next question", "continue_task_id": source.ID, "summary_attempt_id": attempt.ID}
	call("POST", "/v1/tasks", continueBody, 201, &continued)
	var snapshot sessions.Snapshot
	call("GET", "/v1/tasks/"+continued.ID, nil, 200, &snapshot)
	if snapshot.Compaction == nil || snapshot.Compaction.SummaryAttemptID != attempt.ID || snapshot.Compaction.SummaryReviewID != approval.ID {
		t.Fatalf("missing review provenance: %+v", snapshot.Compaction)
	}
	var rejection sessions.SummaryReview
	call("POST", "/v1/summaries/reviews", map[string]any{"attempt_id": attempt.ID, "expected_id": approval.ID, "decision": "rejected", "note": "Found an omission"}, 201, &rejection)
	call("POST", "/v1/summaries/reviews", map[string]any{"attempt_id": attempt.ID, "expected_id": approval.ID, "decision": "approved", "note": "Stale approval"}, 409, nil)
	call("POST", "/v1/tasks", continueBody, 422, nil)
	call("GET", "/v1/summaries/"+attempt.ID+"/reviews", nil, 200, &reviews)
	if len(reviews) != 2 || reviews[1].ID != rejection.ID {
		t.Fatalf("review revisions mismatch: %+v", reviews)
	}
	after, err := sessions.Replay(ctx, db, source.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source snapshot changed", err)
	}
	afterEvents, err := db.Read(ctx, source.ID, 0, 100)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatal("source events changed", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("reviews or denied continuation dispatched inference: %d", calls.Load())
	}
}
