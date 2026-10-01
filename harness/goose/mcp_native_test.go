package goose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness/toolbridge"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// This qualifies only the installed client's private MCP interface. Provider
// proposals are controlled fixtures; SDK/journal integration is tested separately.
func TestNativeGooseMCPRendezvous(t *testing.T) {
	if os.Getenv("NEXUS_GOOSE_NATIVE") != "1" {
		t.Skip("installed native runner qualification is opt-in")
	}
	executable := "/Users/aj_lobster/Documents/Codex/2026-09-19/do-x20/outputs/harness-runtime/goose-1.52.0/goose"
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	var mu sync.Mutex
	var effects []string
	turns := 0
	b, e := toolbridge.NewOrdered(ctx, 8, 10*time.Second, func(_ context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
		mu.Lock()
		effects = append(effects, x.Call.ID)
		mu.Unlock()
		return runtime.ToolResult{Content: "host result " + x.Call.ID, Effect: runtime.NoEffect}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	tools := []providers.Tool{{Name: "lookup", Description: "Host controlled lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}}
	mcp, e := toolbridge.GooseMCP(b, tools)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			mcp.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			http.Error(w, "denied", 403)
			return
		}
		var request struct {
			Tools    []struct{ Function struct{ Name string } }
			Messages []struct {
				Role, Content string
				ID            string `json:"tool_call_id"`
			}
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request) != nil {
			http.Error(w, "bad", 400)
			return
		}
		mu.Lock()
		turns++
		turn := turns
		mu.Unlock()
		delta := map[string]any{"role": "assistant"}
		finish := "stop"
		if turn == 1 {
			if len(request.Tools) != 1 || request.Tools[0].Function.Name != "nexus__lookup" {
				t.Error("unexpected native tools", request.Tools)
			}
			var calls []any
			for i := range 2 {
				id := fmt.Sprintf("host-original-%d", i)
				x := runtime.ToolExecution{TaskID: "task", SessionID: "session", TurnID: "turn", AttemptID: "attempt", Call: providers.ToolCall{ID: id, Name: "lookup", Arguments: json.RawMessage(`{"path":"same.txt"}`)}}
				if e := b.Register(x); e != nil {
					t.Error(e)
				}
				calls = append(calls, map[string]any{"index": i, "id": id, "type": "function", "function": map[string]string{"name": "nexus__lookup", "arguments": string(x.Call.Arguments)}})
			}
			delta["tool_calls"] = calls
			finish = "tool_calls"
		} else if turn == 2 {
			returned := map[string]string{}
			for _, m := range request.Messages {
				if m.Role == "tool" {
					returned[m.ID] = m.Content
				}
			}
			if !reflect.DeepEqual(returned, map[string]string{"host-original-0": "host result host-original-0", "host-original-1": "host result host-original-1"}) {
				t.Error("native result binding", returned)
			}
			delta["content"] = "verified Goose answer"
		} else {
			t.Error("unexpected provider retry", turn)
			http.Error(w, "denied", 409)
			return
		}
		chunk, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("response-%d", turn), "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	dir := t.TempDir()
	recipe := map[string]any{"version": "1.0.0", "title": "Nexus host tools", "description": "Isolated host tool fixture", "instructions": "Use only host tools then answer.", "prompt": "Execute the host-provided task.", "extensions": []any{map[string]any{"type": "streamable_http", "name": "nexus", "uri": server.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + b.Token()}, "timeout": 10}}}
	raw, _ := json.Marshal(recipe)
	path := filepath.Join(dir, "recipe.json")
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	command := exec.CommandContext(ctx, executable, "run", "--quiet", "--no-session", "--provider", "openai", "--model", "fixture", "--max-turns", "3", "--recipe", path, "--output-format", "stream-json")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GOOSE_PATH_ROOT=" + dir, "OPENAI_API_KEY=fixture-key", "OPENAI_BASE_URL=" + server.URL + "/v1", "GOOSE_DISABLE_SESSION_NAMING=true", "NO_COLOR=1"}
	command.Dir = dir
	command.Stderr = io.Discard
	output := &boundedOutput{limit: MaxStreamBytes, cancel: cancel}
	command.Stdout = output
	if e := runProcess(command); e != nil {
		t.Fatal(e, string(output.data))
	}
	if !strings.Contains(string(output.data), "verified Goose answer") || !strings.Contains(string(output.data), `"type":"complete"`) {
		t.Fatal(string(output.data))
	}
	mu.Lock()
	defer mu.Unlock()
	if turns != 2 || !reflect.DeepEqual(effects, []string{"host-original-0", "host-original-1"}) {
		t.Fatal(turns, effects)
	}
}
