package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestExplicitTaskEndToEnd(t *testing.T) {
	key := "fixture-secret"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("wrong request")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request body")
		}
		fmt.Fprintln(w, `{"message":{"content":"fixture-"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":"secret answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s := config.Defaults()
	s.Mode = "local_only"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, APIKeyEnv: "FIXTURE_KEY"}}
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	out, err := RunExplicit(context.Background(), s, Request{ModelID: "chat", Prompt: "do not store " + key}, func(string) string { return key })
	if err != nil || out.Text != "[REDACTED] answer" || out.Turns != 1 || calls != 1 {
		t.Fatalf("%+v %v calls=%d", out, err, calls)
	}
	db, err := telemetry.Open(context.Background(), s.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatal("not durably completed")
	}
	for _, e := range events {
		raw, _ := e.Encode()
		if strings.Contains(string(raw), key) {
			t.Fatal("key persisted")
		}
		if e.Kind == runtime.ModelDelta && e.Data.Text != "" {
			t.Fatal("partial delta persisted")
		}
	}
	s.Models[0].Locality = "cloud"
	if _, err := RunExplicit(context.Background(), s, Request{ModelID: "chat", Prompt: "hello"}, func(string) string { return key }); err == nil || calls != 1 {
		t.Fatal("forbidden cloud task dispatched")
	}
}

func TestAdmissionNoNetwork(t *testing.T) {
	s := config.Defaults()
	s.Mode = "local_only"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "admission.db")
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://192.168.1.2:11434"}}
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	if _, err := RunExplicit(context.Background(), s, Request{ModelID: "chat", Prompt: "hello"}, nil); err != ErrAdmission {
		t.Fatal(err)
	}
}
