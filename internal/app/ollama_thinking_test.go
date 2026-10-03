package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestConfiguredOllamaThinkingControlReachesExecution(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("decode")
			http.Error(w, "decode", 400)
			return
		}
		if string(body["think"]) != "false" {
			t.Error("configured thinking setting not forwarded")
			fmt.Fprintln(w, `{"message":{"thinking":"reasoning only"},"done":true,"done_reason":"stop"}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"thinking":"never expose reasoning","content":"review answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s := config.Defaults()
	s.Mode = "local_only"
	s.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
	v := false
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, OllamaThink: &v}}
	s.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, Capabilities: []string{"chat"}}}
	out, e := RunExplicit(context.Background(), s, Request{ModelID: "chat", Prompt: "review fixture"}, nil)
	if e != nil || out.Text != "review answer" || calls != 1 {
		t.Fatal(out, e, calls)
	}
}
