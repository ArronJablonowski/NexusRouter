package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestModelDraftOverLocalHTTPRemainsInactive(t *testing.T) {
	const output = `{"version":1,"description":"Repeatable test workflow","tags":["code"],"steps":["Run focused tests","Inspect the reported failures"],"required_tools":[],"configuration":"","risks":["Tests may not cover all behavior"],"validation_cases":["Known passing and failing fixtures"]}`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Error("unexpected provider operation", r.Method, r.URL.Path)
		}
		var request struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
			Tools    []json.RawMessage   `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "local-generator" || len(request.Messages) != 2 || len(request.Tools) != 0 {
			t.Error("invalid tools-free generation request")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		_ = encoder.Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": output}, "done": false})
		_ = encoder.Encode(map[string]any{"done": true, "done_reason": "stop", "prompt_eval_count": 20, "eval_count": 15})
	}))
	defer server.Close()
	provider, err := providers.NewHTTP(server.URL, "ollama", "", server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	generator := ModelGenerator{Provider: provider, Model: "local-generator", ContextTokens: 100000, Timeout: time.Second}
	store := openTest(t, testPath(t))
	store.SetAutomatic(true)
	key := Key{Scope: "project", Name: "generated-checks"}
	version, err := store.DraftFromWorkflows(context.Background(), key, validWorkflowExamples(), generator)
	if err != nil || calls.Load() != 1 || version.Draft.Key != key || len(version.Draft.SourceEvidence) != 2 {
		t.Fatal(version, err, calls.Load())
	}
	history, err := store.History(context.Background(), key)
	if err != nil || history.Active != "" || len(history.Activations) != 0 || len(history.Versions) != 1 {
		t.Fatal(history, err)
	}
}
