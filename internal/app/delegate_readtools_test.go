package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestDelegateReadToolsInheritedScopeAndNoRecursion(t *testing.T) {
	for _, mode := range []string{"read", "count", "escape", "recursive", "turn_limit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			for path, text := range map[string]string{filepath.Join(workspace, "note.txt"): "workspace evidence", filepath.Join(root, "outside.txt"): "outside private secret"} {
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "count" {
				if err := os.MkdirAll(filepath.Join(workspace, "nested", "deep"), 0700); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(workspace, "nested", "inside.txt"), filepath.Join(workspace, "nested", "deep", "deeper.txt")} {
					if err := os.WriteFile(path, []byte("private count fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var parents, children atomic.Int32
			parentTool := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string              `json:"model"`
					Messages []providers.Message `json:"messages"`
					Tools    []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad provider request")
					return
				}
				if body.Model == "parent" {
					if parents.Add(1) == 1 {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"Read scoped evidence","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
						return
					}
					for _, m := range body.Messages {
						if m.Role == "tool" {
							parentTool <- m.Content
						}
					}
					fmt.Fprintln(w, `{"message":{"content":"parent final"},"done":true,"done_reason":"stop"}`)
					return
				}
				if body.Model != "child" {
					t.Error("unexpected model")
					return
				}
				n := children.Add(1)
				if len(body.Tools) != 1 || body.Tools[0].Function.Name != "read_file" {
					t.Error("child capability escaped", body.Tools)
				}
				if n == 1 {
					if len(body.Messages) != 1 || body.Messages[0].Content != "Read scoped evidence" {
						t.Error("ambient context escaped", body.Messages)
					}
					if mode == "recursive" {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"delegate","arguments":{"prompt":"nested request","validation":"text"}}}]},"done":true,"done_reason":"tool_calls"}`)
						return
					}
					path := "note.txt"
					if mode == "escape" {
						path = "../outside.txt"
					}
					if mode == "count" {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":".","operation":"count_regular_files"}}}]},"done":true,"done_reason":"tool_calls"}`)
						return
					}
					fmt.Fprintf(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":%q}}}]},"done":true,"done_reason":"tool_calls"}`, path)
					return
				}
				if n != 2 {
					t.Error("extra child inference", n)
				}
				if mode == "turn_limit" {
					fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":"note.txt"}}}]},"done":true,"done_reason":"tool_calls"}`)
					return
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "tool" || strings.Contains(last.Content, "outside private secret") {
					t.Error("unsafe child tool result", last)
				}
				if mode == "read" && !strings.Contains(last.Content, "workspace evidence") {
					t.Error("scoped file missing", last)
				}
				if mode == "escape" && last.Content != "Tool execution failed.\n"+`{"error":"file_unavailable"}` {
					t.Error("escape not rejected", last)
				}
				if mode == "count" {
					for _, expected := range []string{`"evidence":"bounded_root_count_v1"`, `"non_recursive_regular_files":1`, `"recursive_regular_files":3`, `"directories_visited":3`} {
						if !strings.Contains(last.Content, expected) {
							t.Error("aggregate count missing", expected, last)
						}
					}
					for _, private := range []string{"note.txt", "inside.txt", "deeper.txt", "private count fixture", "workspace evidence"} {
						if strings.Contains(last.Content, private) {
							t.Error("aggregate count disclosed private data", private, last)
						}
					}
					fmt.Fprintln(w, `{"message":{"content":"1 direct regular file; 3 recursively"},"done":true,"done_reason":"stop"}`)
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"child final"},"done":true,"done_reason":"stop"}`)
			}))
			defer func() { cancel(); server.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Workers.Max = 2
			cfg.Hardware.Concurrent = "2"
			cfg.Workers.DelegateModel = "child"
			cfg.Workers.DelegateMaxCalls = 1
			cfg.Workers.DelegateReadTools = true
			cfg.Workers.DelegateMaxTurns = 4
			if mode == "turn_limit" {
				cfg.Workers.DelegateMaxTurns = 2
			}
			cfg.Tools.Enabled = true
			cfg.Tools.ReadRoot = workspace
			cfg.Telemetry.Database = filepath.Join(root, "tasks.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
			zero := 0.0
			for _, id := range []string{"parent", "child"} {
				cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}})
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			result, err := svc.Run(ctx, Request{ModelID: "parent", Prompt: "ambient parent data"})
			if err != nil || result.Text != "parent final" || parents.Load() != 2 {
				t.Fatal(result, err, parents.Load())
			}
			var output string
			select {
			case output = <-parentTool:
			default:
				t.Fatal("missing delegate response")
			}
			raw, err := sql.Open("sqlite", "file:"+cfg.Telemetry.Database+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var workers int
			if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE json_extract(body,'$.kind')='worker.started'`).Scan(&workers); err != nil || workers != 1 {
				t.Fatal("nested worker created", workers, err)
			}
			if mode == "recursive" {
				if !validDelegateRejection(output) || children.Load() != 1 {
					t.Fatal("recursive tool accepted", output, children.Load())
				}
				return
			}
			if mode == "turn_limit" {
				if !validDelegateRejection(output) || children.Load() != 2 {
					t.Fatal("child exceeded iteration limit", output, children.Load())
				}
				return
			}
			var envelope struct {
				ExecutionID string `json:"execution_task_id"`
				Output      string `json:"untrusted_output"`
			}
			expectedOutput := "child final"
			if mode == "count" {
				expectedOutput = "1 direct regular file; 3 recursively"
			}
			if json.Unmarshal([]byte(output), &envelope) != nil || envelope.ExecutionID == "" || envelope.Output != expectedOutput || children.Load() != 2 {
				t.Fatal(output, children.Load())
			}
			if mode == "count" {
				for _, private := range []string{"note.txt", "inside.txt", "deeper.txt", "private count fixture", "workspace evidence"} {
					if strings.Contains(output, private) {
						t.Fatal("delegate envelope disclosed private data", private, output)
					}
				}
			}
			db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			snapshot, err := db.TaskSnapshot(ctx, envelope.ExecutionID)
			if err != nil || snapshot.State != "completed" || len(snapshot.Messages) != 4 {
				t.Fatal(snapshot, err)
			}
			events, err := db.Read(ctx, envelope.ExecutionID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			started, completed := 0, 0
			for _, e := range events {
				if e.Kind == runtime.ToolStarted {
					started++
				}
				if e.Kind == runtime.ToolCompleted {
					completed++
				}
				if strings.Contains(e.Data.Text, "outside private secret") {
					t.Fatal("durable secret escaped")
				}
			}
			if started != 1 || completed != 1 {
				t.Fatal("child tool pair missing", started, completed)
			}
		})
	}
}

func TestCloudCoordinatorDelegatesBoundedCountWithoutDirectFilesystemAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(workspace, "one.txt"):             "private one",
		filepath.Join(workspace, "two.txt"):             "private two",
		filepath.Join(workspace, "nested", "three.txt"): "private three",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}

	var childCalls atomic.Int32
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "local-counter" {
			t.Error("invalid local worker request")
			return
		}
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "read_file" {
			t.Error("local worker received the wrong authority", body.Tools)
		}
		if !strings.Contains(string(body.Tools[0].Function.Parameters), `"required":["path","operation"]`) {
			t.Error("cloud-delegated worker received file-content authority", string(body.Tools[0].Function.Parameters))
		}
		if childCalls.Add(1) == 1 {
			fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{"path":".","operation":"count_regular_files"}}}]},"done":true,"done_reason":"tool_calls"}`)
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "tool" || !strings.Contains(last.Content, `"non_recursive_regular_files":2`) || !strings.Contains(last.Content, `"recursive_regular_files":3`) || !strings.Contains(last.Content, `"symlinks_skipped":1`) {
			t.Error("bounded count evidence missing", last)
		}
		for _, private := range []string{"one.txt", "two.txt", "three.txt", "outside.txt", "private one", "outside private"} {
			if strings.Contains(last.Content, private) {
				t.Error("local aggregate disclosed private data", private, last)
			}
		}
		fmt.Fprintln(w, `{"message":{"content":"2 direct regular files; 3 recursively"},"done":true,"done_reason":"stop"}`)
	}))
	defer child.Close()

	var parentCalls atomic.Int32
	parentEvidence := make(chan string, 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if r.URL.Path != "/chat/completions" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "gpt-5.6-sol" {
			t.Error("invalid cloud coordinator request")
			return
		}
		seenDelegate := false
		for _, tool := range body.Tools {
			seenDelegate = seenDelegate || tool.Function.Name == "delegate"
			if tool.Function.Name == "read_file" {
				t.Error("cloud coordinator received direct filesystem authority")
			}
		}
		if !seenDelegate {
			t.Error("cloud coordinator did not receive delegation authority")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if parentCalls.Add(1) == 1 {
			arguments := `{"prompt":"Count regular files at the configured root. Return the direct and recursive totals only.","validation":"text"}`
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"delegate-count\",\"type\":\"function\",\"function\":{\"name\":\"delegate\",\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", arguments)
			return
		}
		for _, message := range body.Messages {
			if message.Role == "tool" {
				parentEvidence <- message.Content
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"There are 2 direct regular files and 3 recursively.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer parent.Close()

	cfg := config.Defaults()
	cfg.Mode = "hybrid"
	cfg.Telemetry.Database = filepath.Join(root, "tasks.db")
	cfg.Workers.Max, cfg.Workers.DelegateMaxCalls = 2, 1
	cfg.Hardware.Concurrent = "2"
	cfg.Workers.DelegateModel = "local-worker"
	cfg.Workers.DelegateReadTools = true
	cfg.Workers.DelegateMaxTurns = 4
	cfg.Tools.Enabled, cfg.Tools.ReadRoot = true, workspace
	cfg.Providers = []config.Provider{{ID: "cloud", Kind: "openai_compatible", Endpoint: parent.URL}, {ID: "local", Kind: "ollama", Endpoint: child.URL}}
	zero := 0.0
	cfg.Models = []config.Model{
		{ID: "coordinator", Model: "gpt-5.6-sol", Provider: "cloud", Locality: "cloud", ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}},
		{ID: "local-worker", Model: "local-counter", Provider: "local", Locality: "local", RAMBytes: 1, ContextTokens: 16384, EstimatedCost: &zero, Capabilities: []string{"chat"}},
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	result, err := svc.Run(ctx, Request{ModelID: "coordinator", Prompt: "How many regular files are in the configured directory?"})
	if err != nil || result.Text != "There are 2 direct regular files and 3 recursively." || parentCalls.Load() != 2 || childCalls.Load() != 2 {
		t.Fatal(result, err, parentCalls.Load(), childCalls.Load())
	}
	var evidence string
	select {
	case evidence = <-parentEvidence:
	default:
		t.Fatal("cloud coordinator did not receive worker evidence")
	}
	var envelope struct {
		Output string `json:"untrusted_output"`
	}
	if json.Unmarshal([]byte(evidence), &envelope) != nil || envelope.Output != "2 direct regular files; 3 recursively" {
		t.Fatal("invalid bounded delegation evidence", evidence)
	}
	for _, private := range []string{"one.txt", "two.txt", "three.txt", "outside.txt", "private one", "outside private"} {
		if strings.Contains(evidence, private) {
			t.Fatal("private filesystem data reached cloud coordinator", private, evidence)
		}
	}
}
