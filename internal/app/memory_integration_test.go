package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func seedContextMemory(t *testing.T, cfg config.Settings) {
	t.Helper()
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, item := range []struct {
		id, scope, privacy, content string
		expired                     bool
	}{
		{"private", "project", "local_only", "private-fact secret-value", false},
		{"shared", "project", "shareable", "shared-fact", false},
		{"other", "elsewhere", "local_only", "wrong-scope", false},
		{"expired", "project", "local_only", "old-fact", true},
	} {
		now := time.Now().UTC()
		f := memory.Fact{Version: 1, ID: item.id, Scope: item.scope, Revision: 1, Content: item.content, Provenance: "operator", Confidence: 1, Privacy: item.privacy, Created: now.Add(-time.Hour), Updated: now.Add(-time.Hour)}
		if item.expired {
			f.Expires = now.Add(-time.Minute)
		}
		if err := db.PutMemory(context.Background(), f, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScopedMemoryReachesLocalModelAndDurableHistory(t *testing.T) {
	svc, cfg := autoFixture(t)
	seedContextMemory(t, cfg)
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body)
		seen = string(encoded)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.settings.Memory.Scope = "project"
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return "secret-value"
		}
		return ""
	}
	out, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "Use my fact preferences"})
	if err != nil {
		t.Fatal(out, err)
	}
	for _, want := range []string{"private-fact", "shared-fact", "[REDACTED]"} {
		if !strings.Contains(seen, want) {
			t.Fatal("missing context", want, seen)
		}
	}
	for _, bad := range []string{"secret-value", "wrong-scope", "old-fact"} {
		if strings.Contains(seen, bad) {
			t.Fatal("memory boundary leak", bad)
		}
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if !strings.Contains(string(encoded), "private-fact") || strings.Contains(string(encoded), "secret-value") {
		t.Fatal("durable context mismatch")
	}
}

func TestMemoryPrivacyPinsHybridAndFiltersCloud(t *testing.T) {
	svc, cfg := autoFixture(t)
	seedContextMemory(t, cfg)
	svc.settings.Mode = "hybrid"
	svc.settings.Models[0].Locality = "cloud"
	svc.settings.Memory.Scope = "project"
	ctx := context.Background()
	out, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "fact"})
	if err != nil || out.Text != "z" {
		t.Fatal("private memory did not pin local", out, err)
	}
	for _, localOnly := range []bool{true, false} {
		svc.settings.Memory.LocalOnly = localOnly
		var seen string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			raw, _ := json.Marshal(body)
			seen = string(raw)
			fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
		}))
		svc.settings.Providers[0].Endpoint = server.URL
		_, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "fact"})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(seen, "private-fact") || strings.Contains(seen, "wrong-scope") || strings.Contains(seen, "old-fact") {
			t.Fatal("private memory reached cloud-designated fixture")
		}
		if strings.Contains(seen, "shared-fact") == localOnly {
			t.Fatal("cloud memory mode ignored", localOnly, seen)
		}
	}
}

func TestAutomaticMemoryDeletedAfterSelectionBlocksDispatch(t *testing.T) {
	svc, cfg := autoFixture(t)
	seedContextMemory(t, cfg)
	svc.settings.Memory.Scope = "project"
	var once sync.Once
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			once.Do(func() {
				db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
				if err != nil {
					t.Error(err)
					return
				}
				defer db.Close()
				if err := db.DeleteMemory(context.Background(), "project", "private", 1); err != nil {
					t.Error(err)
				}
			})
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		seen = string(raw)
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "fact"}); err == nil {
		t.Fatal("deleted snapshot admitted")
	}
	if seen != "" {
		t.Fatal("deleted snapshot reached inference")
	}
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "fact"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "private-fact") {
		t.Fatal("deleted fact leaked into next fresh task")
	}
}
