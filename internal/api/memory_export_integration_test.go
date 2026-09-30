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
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/memory"
)

func memoryExportHTTPFixture(t *testing.T) (*telemetry.Store, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inference.Add(1); w.WriteHeader(500) }))
	t.Cleanup(provider.Close)
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "memory.db")
	cfg.Memory.Scope = "project"
	cfg.Memory.Enabled = false
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "model", Model: "fixture-model", Provider: "local", Locality: "local", ContextTokens: 4096, RAMBytes: 1, Capabilities: []string{"code"}}}
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := app.NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	application := services()
	application.ExportMemory = svc.ExportMemory
	handler, err := New(token, 1, application)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return db, server, &inference
}

func callMemoryExportHTTP(t *testing.T, server *httptest.Server, authorized bool, want int) []byte {
	t.Helper()
	request, err := http.NewRequest("POST", server.URL+"/v1/memory/export", strings.NewReader(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if authorized {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, memory.ExportMaxBytes+1))
	if err != nil || response.StatusCode != want || len(body) > memory.ExportMaxBytes || strings.Contains(string(body), token) {
		t.Fatal(response.StatusCode, want, len(body), err)
	}
	return body
}

func TestMemoryExportHTTPConsistentScopedPrivateExpiredSnapshot(t *testing.T) {
	db, server, inference := memoryExportHTTPFixture(t)
	ctx := context.Background()
	a, b, other, retired := managedMemoryFact(), managedMemoryFact(), managedMemoryFact(), managedMemoryFact()
	a.ID = "a-private"
	a.Content = "legacy " + token
	a.Provenance = "import " + token
	b.ID = "b-expired"
	b.Created = time.Now().UTC().Add(-time.Hour)
	b.Updated = b.Created
	b.Expires = b.Created.Add(time.Minute)
	b.Privacy = "shareable"
	other.Scope = "another-project"
	other.Content = "OUTSIDE_SCOPE"
	retired.ID = "retired"
	retired.Content = "RETIRED_PAYLOAD"
	for _, f := range []memory.Fact{b, a, other, retired} {
		if err := db.PutMemory(ctx, f, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.DeleteMemory(ctx, retired.Scope, retired.ID, 1); err != nil {
		t.Fatal(err)
	}
	callMemoryExportHTTP(t, server, false, http.StatusUnauthorized)
	before := time.Now().UTC()
	body := callMemoryExportHTTP(t, server, true, http.StatusOK)
	after := time.Now().UTC()
	var out memory.ExportSnapshot
	if json.Unmarshal(body, &out) != nil || out.Version != 1 || out.Scope != "project" || out.CapturedAt.Before(before) || out.CapturedAt.After(after) || len(out.Facts) != 2 || out.Facts[0].ID != a.ID || out.Facts[1].ID != b.ID {
		t.Fatal("wrong export envelope", string(body))
	}
	if strings.Contains(string(body), "OUTSIDE_SCOPE") || strings.Contains(string(body), "RETIRED_PAYLOAD") || !strings.Contains(out.Facts[0].Content, "[REDACTED]") || !strings.Contains(out.Facts[0].Provenance, "[REDACTED]") || out.Facts[0].Privacy != "local_only" || out.Facts[1].Expires.IsZero() {
		t.Fatal("scope, privacy, expiry or redaction failure")
	}
	for _, f := range []memory.Fact{a, b, other} {
		got, err := db.GetMemory(ctx, f.Scope, f.ID)
		if err != nil || !reflect.DeepEqual(got, f) {
			t.Fatal("export mutated stored fact/use metadata", err)
		}
	}
	if inference.Load() != 0 {
		t.Fatal("export invoked provider", inference.Load())
	}
}

func TestMemoryExportHTTPEmptySnapshot(t *testing.T) {
	_, server, inference := memoryExportHTTPFixture(t)
	var out memory.ExportSnapshot
	if json.Unmarshal(callMemoryExportHTTP(t, server, true, http.StatusOK), &out) != nil || out.Version != 1 || out.Scope != "project" || out.Facts == nil || len(out.Facts) != 0 {
		t.Fatal(out)
	}
	if inference.Load() != 0 {
		t.Fatal("empty export invoked provider")
	}
}

func TestMemoryExportHTTPBackendOverflowReturnsNoPartialFacts(t *testing.T) {
	db, server, inference := memoryExportHTTPFixture(t)
	ctx := context.Background()
	for i := 0; i < memory.ExportMaxFacts+1; i++ {
		f := managedMemoryFact()
		f.ID = fmt.Sprintf("fact-%04d", i)
		f.Content = "SHOULD_NOT_BE_PARTIALLY_EXPORTED"
		if err := db.PutMemory(ctx, f, 0); err != nil {
			t.Fatal(err)
		}
	}
	body := callMemoryExportHTTP(t, server, true, http.StatusServiceUnavailable)
	if strings.Contains(string(body), "SHOULD_NOT_BE_PARTIALLY_EXPORTED") || strings.Contains(string(body), "fact-0000") || strings.Contains(strings.ToLower(string(body)), "sqlite") {
		t.Fatal("failed snapshot returned partial/private data", string(body))
	}
	if inference.Load() != 0 {
		t.Fatal("overflow invoked provider")
	}
}
