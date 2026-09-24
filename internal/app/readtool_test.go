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
	"syscall"
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
		if out.Failed != (path != "ok.txt") || out.Recoverable != (path != "ok.txt") {
			t.Fatal("incorrect read outcome flags", path, out)
		}
		if path != "ok.txt" && out.Content != `{"error":"file_unavailable"}` {
			t.Fatalf("escape exposed: %s", out.Content)
		}
	}
	if _, err := ex.Execute(context.Background(), providers.ToolCall{ID: "bad", Name: "read_file", Arguments: json.RawMessage(`{"path":"ok.txt","extra":true}`)}); err != tools.ErrArguments {
		t.Fatal(err)
	}
}

func TestReadToolReturnsBoundedAggregateDirectoryCounts(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"first.txt":                          "first private content",
		"second.txt":                         "second private content",
		filepath.Join("nested", "a"):         "nested private content",
		filepath.Join("nested", "b"):         "more nested private content",
		filepath.Join("nested", "deep", "c"): "deep private content",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside-secret.txt"), []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside-secret.txt"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "nested-link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "named-pipe"), 0600); err != nil {
		t.Fatal(err)
	}

	registry, closeRegistry, err := readTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRegistry()
	executor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Allow}}
	result, err := executor.Execute(context.Background(), providers.ToolCall{ID: "count", Name: "read_file", Arguments: json.RawMessage(`{"path":".","operation":"count_regular_files"}`)})
	if err != nil || result.Failed || result.Recoverable || result.Effect != runtime.NoEffect {
		t.Fatal(result, err)
	}
	var count directoryCountResult
	if err := json.Unmarshal([]byte(result.Content), &count); err != nil {
		t.Fatal(err, result.Content)
	}
	if count != (directoryCountResult{
		Evidence:                 "bounded_root_count_v1",
		Path:                     ".",
		NonRecursiveRegularFiles: 2,
		RecursiveRegularFiles:    5,
		DirectoriesVisited:       3,
		SymlinksSkipped:          2,
		OtherEntriesSkipped:      1,
	}) {
		t.Fatalf("unexpected aggregate count: %+v", count)
	}
	for _, secret := range []string{"first.txt", "nested", "outside-secret.txt", "private content", "outside secret"} {
		if strings.Contains(result.Content, secret) {
			t.Fatalf("aggregate evidence disclosed %q: %s", secret, result.Content)
		}
	}

	for _, path := range []string{"../", outside, "outside-link", "nested-link", "first.txt", "missing"} {
		arguments, _ := json.Marshal(readFileArguments{Path: path, Operation: "count_regular_files"})
		denied, err := executor.Execute(context.Background(), providers.ToolCall{ID: "denied", Name: "read_file", Arguments: arguments})
		expected := `{"error":"directory_unavailable"}`
		if !filepath.IsLocal(path) {
			expected = `{"error":"file_unavailable"}`
		}
		if err != nil || !denied.Failed || !denied.Recoverable || denied.Effect != runtime.NoEffect || denied.Content != expected {
			t.Fatalf("%q was not safely denied: %+v %v", path, denied, err)
		}
	}
	if _, err := executor.Execute(context.Background(), providers.ToolCall{ID: "unknown", Name: "read_file", Arguments: json.RawMessage(`{"path":".","operation":"shell"}`)}); err != tools.ErrArguments {
		t.Fatalf("unknown operation accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	denied, err := executor.Execute(canceled, providers.ToolCall{ID: "canceled", Name: "read_file", Arguments: json.RawMessage(`{"path":".","operation":"count_regular_files"}`)})
	if err != context.Canceled || denied.Content != "" || denied.Effect != runtime.NoEffect {
		t.Fatalf("canceled count was not denied before dispatch: %+v %v", denied, err)
	}
}

func TestCloudDelegatedCountToolRejectsFileContentReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, closeRegistry, err := delegatedCountTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRegistry()
	catalog := registry.Catalog()
	if len(catalog) != 1 || catalog[0].Name != "read_file" || !strings.Contains(string(catalog[0].Parameters), `"required":["path","operation"]`) {
		t.Fatalf("count-only schema widened: %+v", catalog)
	}
	executor := tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Allow}}
	for _, arguments := range []string{`{"path":"private.txt"}`, `{"path":"private.txt","operation":"count_regular_files"}`} {
		result, err := executor.Execute(context.Background(), providers.ToolCall{ID: "content-denied", Name: "read_file", Arguments: json.RawMessage(arguments)})
		if err == nil && (!result.Failed || strings.Contains(result.Content, "private content")) {
			t.Fatalf("count-only tool disclosed file content: %+v", result)
		}
	}
	result, err := executor.Execute(context.Background(), providers.ToolCall{ID: "count", Name: "read_file", Arguments: json.RawMessage(`{"path":".","operation":"count_regular_files"}`)})
	if err != nil || result.Failed || !strings.Contains(result.Content, `"non_recursive_regular_files":1`) || strings.Contains(result.Content, "private.txt") {
		t.Fatalf("count-only tool did not return aggregate evidence: %+v %v", result, err)
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

func TestOptionalReadToolsDoNotBlockIneligibleModels(t *testing.T) {
	for _, locality := range []string{"cloud", "local"} {
		t.Run(locality, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Tools) != 0 {
					t.Errorf("ineligible model received file tools: %+v, %v", request, err)
				}
				fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
			}))
			defer server.Close()
			cfg := config.Defaults()
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "task.db")
			cfg.Providers = []config.Provider{{ID: "fixture", Kind: "ollama", Endpoint: server.URL}}
			cfg.Models = []config.Model{{ID: "m", Provider: "fixture", Model: "m", Locality: locality, RAMBytes: 1, Capabilities: []string{"chat"}}}
			if locality == "cloud" {
				cfg.Models[0].ContextTokens = 8192
			}
			out, err := RunExplicit(context.Background(), cfg, Request{ModelID: "m", Prompt: "Answer normally"}, nil)
			if err != nil || out.Text != "answer" {
				t.Fatal(out, err)
			}
		})
	}
}
