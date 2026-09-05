package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestReadToolConfinement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	r, close, err := readTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	ex := tools.Executor{Registry: r, Policy: &tools.Policy{Default: tools.Allow}}
	for _, path := range []string{"ok.txt", "../private", outside, "escape", ".", "missing"} {
		args, _ := json.Marshal(map[string]string{"path": path})
		out, err := ex.Execute(context.Background(), providers.ToolCall{ID: "call", Name: "read_file", Arguments: args})
		if err != nil || out.Effect != runtime.NoEffect {
			t.Fatalf("%s: %v", path, err)
		}
		if path == "ok.txt" && !strings.Contains(out.Content, "evidence") {
			t.Fatal(out)
		}
		if path != "ok.txt" && out.Content != `{"error":"file_unavailable"}` {
			t.Fatalf("escape exposed: %s", out.Content)
		}
	}
	if _, err := ex.Execute(context.Background(), providers.ToolCall{ID: "bad", Name: "read_file", Arguments: json.RawMessage(`{"path":"ok.txt","extra":true}`)}); err != tools.ErrArguments {
		t.Fatal(err)
	}
}

func TestApplicationReadToolCycleIsDurable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("workspace evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []providers.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
		}
		calls++
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "read_file" {
			t.Errorf("missing catalog: %+v", req.Tools)
		}
		if calls == 1 {
			fmt.Fprintln(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"note.txt"}}}]},"done":true,"done_reason":"stop"}`)
		} else {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || !strings.Contains(last.Content, "workspace evidence") {
				t.Errorf("no tool result: %+v", last)
			}
			fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Accepted evidence"},"done":true,"done_reason":"stop"}`)
		}
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = dir
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	cfg.Models = []config.Model{{ID: "m", Provider: "local", Model: "m", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	out, err := RunExplicit(context.Background(), cfg, Request{ModelID: "m", Prompt: "Read note.txt"}, nil)
	if err != nil || out.Turns != 2 || calls != 2 || out.Text != "Accepted evidence" {
		t.Fatalf("%+v calls=%d err=%v", out, calls, err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := sessions.Replay(context.Background(), db, out.TaskID)
	if err != nil || snapshot.State != "completed" || snapshot.UncertainEffects || len(snapshot.Pending) != 0 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	found := false
	for _, m := range snapshot.Messages {
		found = found || m.Role == "tool" && strings.Contains(m.Content, "workspace evidence")
	}
	if !found {
		t.Fatal("tool result not durable")
	}
}

func TestFileToolsDenyCloudBeforeStorage(t *testing.T) {
	_, cfg := autoFixture(t)
	cfg.Mode = "hybrid"
	cfg.Models[0].Locality = "cloud"
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = t.TempDir()
	if _, err := RunExplicit(context.Background(), cfg, Request{ModelID: "a", Prompt: "read"}, nil); err != ErrAdmission {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatalf("denied request touched database: %v", err)
	}
}
