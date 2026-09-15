package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestHTTPCompactedContinuationPersistsBeforeProvider(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "compaction-http.db")
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dsn := (&url.URL{Scheme: "file", Path: cfg.Telemetry.Database, RawQuery: "mode=ro"}).String()
	inspection, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	type observed struct {
		messages   []providers.Message
		compaction *runtime.ContextCompaction
	}
	observations := make(chan observed, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []providers.Message `json:"messages"`
		}
		if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("unexpected provider request")
			http.Error(w, "bad request", 400)
			return
		}
		observation := observed{messages: input.Messages}
		// A separate read-only connection must see the task-start compaction
		// before this provider returns any model output.
		rows, err := inspection.QueryContext(r.Context(), "SELECT body FROM events WHERE sequence=1")
		if err != nil {
			t.Error(err)
			http.Error(w, "storage", 500)
			return
		}
		for rows.Next() {
			var body []byte
			var event runtime.Event
			if err := rows.Scan(&body); err != nil {
				t.Error(err)
				continue
			}
			if err := json.Unmarshal(body, &event); err != nil {
				t.Error(err)
				continue
			}
			if event.Kind == runtime.TaskStarted && event.Data.Compaction != nil {
				observation.compaction = event.Data.Compaction
			}
		}
		if err := rows.Err(); err != nil {
			t.Error(err)
		}
		rows.Close()
		observations <- observation
		fmt.Fprintln(w, `{"message":{"content":"retained answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192}}
	svc, err := newAPIFixtureService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(token, 1, Services{
		Run:           svc.Run,
		RunSubmission: fixedIdempotent(svc.Run),
		Inspect:       func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:        func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	requestNumber := 0
	call := func(method, path, body string, status int, output any) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if method == http.MethodPost && path == "/v1/tasks" {
			requestNumber++
			r.Header.Set("Idempotency-Key", fmt.Sprintf("compaction-task-%016d", requestNumber))
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("HTTP status=%d want=%d", response.StatusCode, status)
		}
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	var first, second struct {
		TaskID string `json:"task_id"`
	}
	call("POST", "/v1/tasks", `{"model_id":"chat","prompt":"old detail to remove"}`, 201, &first)
	initialDispatch := <-observations
	if initialDispatch.compaction != nil {
		t.Fatal("initial task unexpectedly compacted")
	}
	var source sessions.Snapshot
	call("GET", "/v1/tasks/"+first.TaskID, "", 200, &source)
	body, err := json.Marshal(map[string]any{"model_id": "chat", "prompt": "next question", "continue_task_id": first.TaskID, "compaction": sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"operator chosen summary"}}}})
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/tasks", string(body), 201, &second)
	dispatch := <-observations
	encoded, err := json.Marshal(dispatch.messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "old detail to remove") || !strings.Contains(string(encoded), "operator chosen summary") || !strings.Contains(string(encoded), "retained answer") || !strings.Contains(string(encoded), "next question") {
		t.Fatalf("incorrect compacted provider context: %s", encoded)
	}
	if dispatch.compaction == nil || dispatch.compaction.SourceTaskID != first.TaskID || dispatch.compaction.SourceSequence != source.Sequence || dispatch.compaction.RemovedMessages != 1 {
		t.Fatalf("compaction not durable before inference: %#v", dispatch.compaction)
	}
	var continued, unchanged sessions.Snapshot
	call("GET", "/v1/tasks/"+second.TaskID, "", 200, &continued)
	call("GET", "/v1/tasks/"+first.TaskID, "", 200, &unchanged)
	if !reflect.DeepEqual(source, unchanged) || source.Messages[0].Content != "old detail to remove" {
		t.Fatal("source history changed")
	}
	if continued.State != "completed" || continued.ParentTaskID != first.TaskID || continued.SessionID != source.SessionID || !reflect.DeepEqual(continued.Compaction, dispatch.compaction) {
		t.Fatalf("durable compaction inspection differs: %#v", continued)
	}
	var third struct {
		TaskID string `json:"task_id"`
	}
	secondBody, err := json.Marshal(map[string]any{"model_id": "chat", "prompt": "third question", "continue_task_id": second.TaskID, "compaction": sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"newer operator summary"}}}})
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/tasks", string(secondBody), 201, &third)
	thirdDispatch := <-observations
	if thirdDispatch.compaction == nil || thirdDispatch.compaction.Version != 2 || thirdDispatch.compaction.SourceTaskID != second.TaskID {
		t.Fatal("second compaction epoch did not reach provider", thirdDispatch.compaction)
	}
	var twiceCompacted sessions.Snapshot
	call("GET", "/v1/tasks/"+third.TaskID, "", 200, &twiceCompacted)
	if twiceCompacted.ContextLineage == nil || len(twiceCompacted.ContextLineage.Epochs) != 2 || twiceCompacted.Compaction == nil || !reflect.DeepEqual(twiceCompacted.Compaction, thirdDispatch.compaction) {
		t.Fatal("HTTP inspection lost multi-epoch lineage", twiceCompacted)
	}
	encodedTwice, _ := json.Marshal(twiceCompacted.Messages)
	if strings.Count(string(encodedTwice), "The following session_summary is an operator-supplied summary") != 1 || strings.Contains(string(encodedTwice), "operator chosen summary") {
		t.Fatal("HTTP continuation retained obsolete summary envelope", string(encodedTwice))
	}
}
