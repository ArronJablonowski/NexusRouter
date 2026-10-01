package textgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type gatewayTools struct {
	invoke func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error)
}

func (g gatewayTools) Execute(context.Context, providers.ToolCall) (runtime.ToolResult, error) {
	panic("unscoped")
}
func (g gatewayTools) ExecuteScoped(c context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
	return g.invoke(c, x)
}
func (g gatewayTools) ToolBehavior(string) runtime.ToolBehavior { return runtime.BehaviorReadOnly }
func agentConfig() AgentConfig {
	return AgentConfig{Config: Config{BaseURL: "https://provider.invalid/v1", Model: "model", APIKey: "provider-secret", ContextTokens: 32768, MaxOutputTokens: 1024, Timeout: 5 * time.Second, Messages: []providers.Message{{Role: "system", Content: "host instructions"}, {Role: "user", Content: "host task"}}}, Actual: harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "native-tools-v1", Provider: "local", Model: "model", ModelRevision: "weights", ConfigSHA256: strings.Repeat("a", 64)}, Tools: []providers.Tool{{Name: "read", Description: "read fixture", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}}}
}
func agentRequest(c AgentConfig, tools gatewayTools) runtime.HarnessAgentRequest {
	return runtime.HarnessAgentRequest{Request: runtime.HarnessRequest{TaskID: "gateway", SessionID: "gateway", Attribution: runtime.HarnessAttribution{Identity: c.Actual, Task: harness.TaskClass{Domain: "code", Profile: "fixture", Difficulty: "hard"}}, Messages: c.Messages, ContextTokens: c.ContextTokens, MaxOutputBytes: 65536, OutputView: func(s string) string { return strings.ReplaceAll(s, "secret", "[redacted]") }}, Tools: tools, MaxTurns: 3}
}
func agentHTTP(t *testing.T, base, path, key, body string) (int, string) {
	t.Helper()
	r, e := http.NewRequest("POST", base+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Untrusted", "must-not-forward")
	response, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	return response.StatusCode, string(data)
}
func agentDB(t *testing.T) *telemetry.Store {
	t.Helper()
	db, e := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "events.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func oneAgentTool() string {
	return agentChunk(agentCall(0, "call-1", "read", `{"path":"host.txt"}`), nil) + agentChunk(`{}`, "tool_calls") + "data: [DONE]\n\n"
}
func TestAgentGatewayDurableHostConversation(t *testing.T) {
	for _, mode := range []string{"normal", "recoverable", "end"} {
		t.Run(mode, func(t *testing.T) {
			db := agentDB(t)
			c := agentConfig()
			var dispatches, effects atomic.Int32
			c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
				n := dispatches.Add(1)
				events, e := db.Read(r.Context(), "gateway", 0, 20)
				if e != nil || events[len(events)-1].Kind != runtime.TurnStarted {
					t.Error("dispatch before committed turn", e)
				}
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "forged") || r.Header.Get("X-Untrusted") != "" || r.Header.Get("Authorization") != "Bearer provider-secret" || r.URL.String() != "https://provider.invalid/v1/chat/completions" {
					t.Error("child authority leaked upstream", string(b))
				}
				if !strings.Contains(string(b), "host task") || !strings.Contains(string(b), "host instructions") {
					t.Error("lost host context")
				}
				if n == 2 {
					if !strings.Contains(string(b), `"tool_call_id":"call-1"`) || !strings.Contains(string(b), "[redacted] result") {
						t.Error("lost durable tool context", string(b))
					}
					if mode == "recoverable" && !strings.Contains(string(b), "Tool execution failed.") {
						t.Error("lost failure status")
					}
					if mode == "end" && strings.Contains(string(b), `"tools"`) {
						t.Error("tools still offered after end")
					}
				}
				stream := oneAgentTool()
				if n == 2 {
					stream = completionFixture("model")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
			})
			q := agentRequest(c, gatewayTools{func(ctx context.Context, x runtime.ToolExecution) (runtime.ToolResult, error) {
				effects.Add(1)
				events, e := db.Read(ctx, "gateway", 0, 20)
				if e != nil || len(events) != 4 || events[3].Kind != runtime.ToolStarted || events[2].Kind != runtime.TurnCompleted || string(x.Call.Arguments) != `{"path":"host.txt"}` {
					t.Error("tool lacks durable verified proposal", e)
				}
				return runtime.ToolResult{Content: "secret result", Effect: runtime.NoEffect, Failed: mode == "recoverable", Recoverable: mode == "recoverable", EndToolUse: mode == "end"}, nil
			}})
			q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				c.Session = s
				g, e := StartAgent(ctx, c)
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				defer g.Close()
				if g.Token == g.ToolToken || g.Token == c.APIKey {
					t.Fatal("shared credentials")
				}
				// Mutating the caller's initial context/schema cannot change the snapshot.
				c.Messages[0].Content = "forged mutation"
				c.Tools[0].Name = "forged-tool"
				child := strings.Replace(gatewayBody, "answer", "forged history", 1)
				if code, _ := agentHTTP(t, g.BaseURL, "/chat/completions", c.APIKey, child); code != 403 {
					t.Fatal(code)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, `{"call_id":"call-1"}`); code != 409 {
					t.Fatal("unregistered call", code)
				}
				if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, child); code != 200 || !strings.Contains(body, "call-1") {
					t.Fatal(code, body)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, child); code != 409 {
					t.Fatal("duplicate inference", code)
				}
				for range 2 {
					if code, body := agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, `{"call_id":"call-1"}`); code != 200 || strings.Contains(body, "secret") || !strings.Contains(body, "[redacted] result") {
						t.Fatal(code, body)
					}
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, " \n"+child); code != 409 {
					t.Fatal("whitespace replay", code)
				}
				next := strings.Replace(child, "forged history", "forged tool result", 1)
				if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, next); code != 200 {
					t.Fatal(code, body)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, gatewayBody); code != 409 {
					t.Fatal("postfinal inference", code)
				}
				text, e := g.Final()
				return runtime.HarnessOutput{Actual: c.Actual, Text: text}, e
			}
			out, text, e := runtime.RunHarnessAgent(context.Background(), db, q)
			if e != nil || out.Status != "completed" || text != "answer" || dispatches.Load() != 2 || effects.Load() != 1 {
				t.Fatal(out, text, e, dispatches.Load(), effects.Load())
			}
			events, e := db.Read(context.Background(), "gateway", 0, 20)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = runtime.ValidateHarnessOutcome(events, "gateway"); e != nil {
				t.Fatal("invalid final journal", e)
			}
		})
	}
}

func TestAgentGatewayFailedDeliveryCannotRetryOrExecute(t *testing.T) {
	for _, mode := range []string{"truncated", "wrong_model", "unknown_tool", "transport", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			db := agentDB(t)
			c := agentConfig()
			var dispatches, effects atomic.Int32
			c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
				dispatches.Add(1)
				if mode == "transport" {
					return nil, errors.New("denied")
				}
				stream := oneAgentTool()
				status := 200
				switch mode {
				case "truncated":
					stream = strings.Replace(stream, "data: [DONE]\n\n", "", 1)
				case "wrong_model":
					stream = strings.ReplaceAll(stream, `"model":"model"`, `"model":"fallback"`)
				case "unknown_tool":
					stream = strings.ReplaceAll(stream, `"name":"read"`, `"name":"shell"`)
				case "redirect":
					status = 302
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Location": []string{"https://other.invalid"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
			})
			q := agentRequest(c, gatewayTools{func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				effects.Add(1)
				return runtime.ToolResult{}, nil
			}})
			q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				c.Session = s
				g, e := StartAgent(ctx, c)
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				defer g.Close()
				if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, gatewayBody); code != 502 || strings.Contains(body, "call-1") {
					t.Fatal(code, body)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, strings.Replace(gatewayBody, "answer", "retry", 1)); code != 409 {
					t.Fatal("failed dispatch replayed", code)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, `{"call_id":"call-1"}`); code != 409 {
					t.Fatal("failed proposal registered", code)
				}
				text, e := g.Final()
				if e == nil {
					t.Fatal("false final", text)
				}
				return runtime.HarnessOutput{}, e
			}
			if _, _, e := runtime.RunHarnessAgent(context.Background(), db, q); e == nil || dispatches.Load() != 1 || effects.Load() != 0 {
				t.Fatal(e, dispatches.Load(), effects.Load())
			}
		})
	}
}

type agentFailJournal struct {
	runtime.Journal
	after bool
}

func (j agentFailJournal) Append(ctx context.Context, seq int64, e runtime.Event) error {
	if seq == 2 {
		if j.after {
			if err := j.Journal.Append(ctx, seq, e); err != nil {
				return err
			}
		}
		return errors.New("ambiguous proposal persistence")
	}
	return j.Journal.Append(ctx, seq, e)
}
func TestAgentGatewayNeverReleasesUnacknowledgedCommit(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			db := agentDB(t)
			c := agentConfig()
			var effects atomic.Int32
			c.Transport = policyTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(oneAgentTool()))}, nil
			})
			q := agentRequest(c, gatewayTools{func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
				effects.Add(1)
				return runtime.ToolResult{}, nil
			}})
			q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
				c.Session = s
				g, e := StartAgent(ctx, c)
				if e != nil {
					return runtime.HarnessOutput{}, e
				}
				defer g.Close()
				if code, body := agentHTTP(t, g.BaseURL, "/chat/completions", g.Token, gatewayBody); code != 502 || strings.Contains(body, "call-1") {
					t.Fatal(code, body)
				}
				if code, _ := agentHTTP(t, g.BaseURL, "/tool", g.ToolToken, `{"call_id":"call-1"}`); code != 409 {
					t.Fatal("unacknowledged call escaped", code)
				}
				return runtime.HarnessOutput{}, nil // swallowed adapter error must remain sticky
			}
			if _, _, e := runtime.RunHarnessAgent(context.Background(), agentFailJournal{db, after}, q); !errors.Is(e, runtime.ErrPersistence) {
				t.Fatal(e)
			}
			events, e := db.Read(context.Background(), "gateway", 0, 20)
			want := 2
			if after {
				want = 3
			}
			if e != nil || len(events) != want || effects.Load() != 0 {
				t.Fatal("appended after persistence ambiguity", len(events), e)
			}
		})
	}
}

func TestAgentGatewayCloseCancelsAndJoinsProvider(t *testing.T) {
	db := agentDB(t)
	c := agentConfig()
	started, exited := make(chan struct{}), make(chan struct{})
	c.Transport = policyTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(exited)
		return nil, r.Context().Err()
	})
	q := agentRequest(c, gatewayTools{func(context.Context, runtime.ToolExecution) (runtime.ToolResult, error) {
		t.Error("unexpected tool")
		return runtime.ToolResult{}, nil
	}})
	q.Execute = func(ctx context.Context, s *runtime.HarnessAgentSession) (runtime.HarnessOutput, error) {
		c.Session = s
		g, e := StartAgent(ctx, c)
		if e != nil {
			return runtime.HarnessOutput{}, e
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			r, _ := http.NewRequest("POST", g.BaseURL+"/chat/completions", strings.NewReader(gatewayBody))
			r.Header.Set("Authorization", "Bearer "+g.Token)
			response, _ := http.DefaultClient.Do(r)
			if response != nil {
				response.Body.Close()
			}
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("dispatch did not start")
		}
		g.Close()
		g.Close()
		select {
		case <-exited:
		default:
			t.Fatal("close returned before transport stopped")
		}
		<-done
		_, e = g.Final()
		if e == nil {
			t.Fatal("accepted canceled dispatch")
		}
		return runtime.HarnessOutput{}, e
	}
	if _, _, e := runtime.RunHarnessAgent(context.Background(), db, q); e == nil {
		t.Fatal("false completion")
	}
}
