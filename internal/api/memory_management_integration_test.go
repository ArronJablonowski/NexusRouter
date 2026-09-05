package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestMemoryManagementHTTPServiceSQLite(t *testing.T) {
	var inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inference.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "memory.db")
	cfg.Memory.Scope = "project"
	cfg.Memory.Enabled = false // A retrieval kill switch must not prevent cleanup.
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "model", Model: "gemma4:12b", Provider: "local", Locality: "local", ContextTokens: 4096, RAMBytes: 1, Capabilities: []string{"code"}}}
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other := managedMemoryFact()
	other.Scope = "another-project"
	other.Content = "OUTSIDE_SCOPE"
	if err := db.PutMemory(ctx, other, 0); err != nil {
		t.Fatal(err)
	}
	svc, err := app.NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.Memory, s.Memories, s.PutMemory, s.DeleteMemory = svc.Memory, svc.Memories, svc.PutMemory, svc.DeleteMemory
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	call := func(action string, body any, want int) []byte {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := http.NewRequest("POST", server.URL+"/v1/memory/"+action, strings.NewReader(string(encoded)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != want || strings.Contains(string(out), token) || strings.Contains(string(out), "OUTSIDE_SCOPE") {
			t.Fatalf("%s: status %d want %d, err %v", action, response.StatusCode, want, err)
		}
		return out
	}
	fact := managedMemoryFact()
	fact.Content = "Prefers Go " + token
	put := func(f memory.Fact, expected int64) map[string]any {
		return map[string]any{"version": 1, "fact": f, "expected_revision": expected}
	}
	call("put", put(fact, 0), 200)
	stored, err := db.GetMemory(ctx, "project", "fact")
	if err != nil || strings.Contains(stored.Content, token) || !strings.Contains(stored.Content, "[REDACTED]") || !stored.LastUse.IsZero() {
		t.Fatal("write redaction or use metadata failed", err)
	}
	var got memory.Fact
	if json.Unmarshal(call("get", map[string]any{"version": 1, "id": "fact"}, 200), &got) != nil || got != stored {
		t.Fatal("inspection changed fact")
	}
	var page struct {
		Version int           `json:"version"`
		Facts   []memory.Fact `json:"facts"`
	}
	query := map[string]any{"version": 1, "after_id": "", "contains": "Go", "limit": 10, "include_expired": false}
	if json.Unmarshal(call("query", query, 200), &page) != nil || page.Version != 1 || len(page.Facts) != 1 || page.Facts[0] != stored {
		t.Fatal("scope-bound query failed")
	}
	updated := stored
	updated.Revision++
	updated.Updated = updated.Updated.Add(time.Second)
	updated.Content = "Prefers Go with tests"
	call("put", put(updated, 1), 200)
	call("put", put(updated, 1), 409)
	call("put", put(other, 0), 503) // Cross-scope authority is never accepted.
	call("delete", map[string]any{"version": 1, "id": "fact", "expected_revision": 1}, 409)
	call("delete", map[string]any{"version": 1, "id": "fact", "expected_revision": 2}, 200)
	call("get", map[string]any{"version": 1, "id": "fact"}, 409)
	call("put", put(fact, 0), 409) // A deleted ID cannot reset its CAS revision.
	if remaining, err := db.GetMemory(ctx, other.Scope, other.ID); err != nil || remaining != other || inference.Load() != 0 {
		t.Fatal("unrelated scope changed or provider invoked", err, inference.Load())
	}
}
