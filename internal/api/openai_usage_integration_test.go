package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestOpenAIUsageRealProviderTwoTurnDurableIntegration(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_first_turn_%v", missing), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release := make(chan struct{})
			var turns, toolCalls atomic.Int32
			prefix := strings.Repeat("live content ", 64)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-ndjson")
				if turns.Add(1) == 1 {
					usage := `,"prompt_eval_count":3,"eval_count":4`
					if missing {
						usage = ""
					}
					fmt.Fprintf(w, `{"message":{"content":"","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":true,"done_reason":"tool_calls"%s}`+"\n", usage)
					return
				}
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":false}\n", prefix)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"finished"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":6}`)
			}))
			defer func() { cancel(); provider.Close() }()
			cfg := config.Defaults()
			cfg.Mode = "local_only"
			cfg.Telemetry.Database = filepath.Join(t.TempDir(), "usage.db")
			cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
			cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
			ext, err := tools.NewExtension([]tools.Definition{{Tool: providers.Tool{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "fixture", ReadOnly: true, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				toolCalls.Add(1)
				return runtime.ToolResult{Content: "evidence", Effect: runtime.NoEffect}, nil
			}}}, &tools.Policy{Default: tools.Allow})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Hardware.AutoProfile = false
			svc, err := app.NewServiceWithToolExtension(cfg, nil, apiFixtureProfiler{}, nil, nil, nil, ext)
			if err != nil {
				t.Fatal(err)
			}
			completed := make(chan app.Result, 1)
			service := services()
			service.Run = svc.Run
			service.RunTextStream = func(ctx context.Context, r app.Request, emit func(string) error) (app.Result, error) {
				out, err := svc.RunTextStream(ctx, r, emit)
				if err == nil {
					db, openErr := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
					if openErr != nil {
						return out, openErr
					}
					snapshot, snapshotErr := db.TaskSnapshot(ctx, out.TaskID)
					db.Close()
					if snapshotErr != nil || snapshot.State != "completed" {
						return out, fmt.Errorf("completion was not durable: %v", snapshotErr)
					}
					completed <- out
				}
				return out, err
			}
			h, err := New(token, 1, service)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer func() { cancel(); server.Close() }()
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"chat","messages":[{"role":"user","content":"use lookup then answer"}],"stream":true,"stream_options":{"include_usage":true}}`))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 || response.Header.Get("X-Darwin-Usage-Scope") != "successful-task-model-turns" {
				t.Fatal(response.Status, response.Header)
			}
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			live, finish, usageSeen, done, failed := false, false, false, false, false
			var durable *app.Result
			checkDurable := func() {
				t.Helper()
				if durable == nil {
					select {
					case out := <-completed:
						durable = &out
					default:
						t.Fatal("metadata emitted before durable completion")
					}
				}
			}
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					if !usageSeen || !finish || failed || done {
						t.Fatal("invalid DONE ordering")
					}
					done = true
					continue
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						Finish *string `json:"finish_reason"`
					} `json:"choices"`
					Usage json.RawMessage `json:"usage"`
					Error *struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					t.Fatal(data, err)
				}
				if chunk.Error != nil {
					checkDurable()
					if !missing || chunk.Error.Code != "usage_unavailable" || finish || usageSeen || done {
						t.Fatal("unexpected usage failure", data)
					}
					failed = true
					continue
				}
				if len(chunk.Choices) == 0 {
					checkDurable()
					if !finish || usageSeen || done || failed {
						t.Fatal("usage frame ordering")
					}
					var counts map[string]int64
					if json.Unmarshal(chunk.Usage, &counts) != nil || len(counts) != 3 || counts["prompt_tokens"] != 8 || counts["completion_tokens"] != 10 || counts["total_tokens"] != 18 {
						t.Fatal("incorrect summed usage", data)
					}
					usageSeen = true
					continue
				}
				if string(chunk.Usage) != "null" {
					t.Fatal("regular chunk invented usage", data)
				}
				for _, choice := range chunk.Choices {
					if choice.Finish != nil {
						checkDurable()
						if *choice.Finish != "stop" || finish || usageSeen {
							t.Fatal("invalid finish", data)
						}
						finish = true
					}
					if choice.Delta.Content != "" && !live {
						live = true
						if finish || usageSeen || done {
							t.Fatal("usage before live content")
						}
						select {
						case <-completed:
							t.Fatal("provider completed before live content barrier")
						default:
						}
						close(release)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if !live || durable == nil || turns.Load() != 2 || toolCalls.Load() != 1 || durable.Turns != 2 {
				t.Fatal("missing real two-turn execution", live, durable, turns.Load(), toolCalls.Load())
			}
			if missing {
				if !failed || done || finish || usageSeen || durable.Usage != nil {
					t.Fatal("unknown usage invented totals")
				}
			} else if failed || !done || !finish || !usageSeen || durable.Usage == nil || durable.Usage.InputTokens != 8 || durable.Usage.OutputTokens != 10 {
				t.Fatal("known totals lost")
			}
		})
	}
}
