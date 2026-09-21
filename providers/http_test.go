package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureProvider(t *testing.T, kind string, handler http.HandlerFunc) *HTTP {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := NewHTTP(server.URL, kind, "fixture-secret", server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func request() Request {
	return Request{Model: "fixture", Messages: []Message{{Role: "user", Content: "hello"}}}
}

func TestOpenAIStream(t *testing.T) {
	p := fixtureProvider(t, "openai_compatible", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("bad request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "fixture" || body["stream"] != true {
			t.Error(body)
		}
		fmt.Fprint(w, ": keepalive\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call1\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Go\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	})
	var chunks []Chunk
	err := p.Stream(context.Background(), request(), func(c Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 || chunks[0].Text != "hello" || chunks[1].Usage.InputTokens != 3 || chunks[2].ToolCall.Name != "lookup" || string(chunks[2].ToolCall.Arguments) != `{"q":"Go"}` || !chunks[3].Done {
		t.Fatalf("bad stream: %+v", chunks)
	}
}

func TestOllamaStreamAndToolHandoff(t *testing.T) {
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Error(r.URL.Path)
		}
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Messages) == 3 && body.Messages[2]["tool_name"] != "lookup" {
			t.Error("missing Ollama tool name")
		}
		fmt.Fprintln(w, `{"message":{"content":"hello","tool_calls":[{"function":{"name":"lookup","arguments":{"q":"Go"}}}]},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`)
	})
	var ids []string
	for range 2 {
		r := request()
		r.Messages = append(r.Messages, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "prior", Name: "lookup", Arguments: json.RawMessage(`{}`)}}}, Message{Role: "tool", ToolCallID: "prior", Content: "ok"})
		var chunks []Chunk
		if err := p.Stream(context.Background(), r, func(c Chunk) error { chunks = append(chunks, c); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(chunks) != 4 || chunks[1].ToolCall == nil || chunks[2].Usage.OutputTokens != 2 || !chunks[3].Done {
			t.Fatalf("bad chunks: %+v", chunks)
		}
		ids = append(ids, chunks[1].ToolCall.ID)
	}
	if ids[0] == ids[1] {
		t.Fatal("tool IDs reused across turns")
	}
}

func TestOutputTokenCeilingWireFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
	}{
		{name: "openai compatible", kind: "openai_compatible"},
		{name: "ollama", kind: "ollama"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int64{0, 73, MaxOutputTokens} {
				t.Run(fmt.Sprint(limit), func(t *testing.T) {
					p := fixtureProvider(t, tc.kind, func(w http.ResponseWriter, r *http.Request) {
						var body struct {
							MaxTokens *int64 `json:"max_tokens"`
							Options   *struct {
								NumPredict *int64 `json:"num_predict"`
							} `json:"options"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						if tc.kind == "openai_compatible" {
							if body.Options != nil {
								t.Fatalf("unexpected Ollama options: %+v", body.Options)
							}
							if limit == 0 && body.MaxTokens != nil {
								t.Fatalf("zero ceiling sent as %d", *body.MaxTokens)
							}
							if limit > 0 && (body.MaxTokens == nil || *body.MaxTokens != limit) {
								t.Fatalf("max_tokens = %v, want %d", body.MaxTokens, limit)
							}
							fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
							return
						}
						if body.MaxTokens != nil {
							t.Fatalf("unexpected max_tokens: %d", *body.MaxTokens)
						}
						if limit == 0 && body.Options != nil {
							t.Fatalf("zero ceiling sent as options: %+v", body.Options)
						}
						if limit > 0 && (body.Options == nil || body.Options.NumPredict == nil || *body.Options.NumPredict != limit) {
							t.Fatalf("options.num_predict = %+v, want %d", body.Options, limit)
						}
						fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop"}`)
					})
					r := request()
					r.MaxOutputTokens = limit
					if err := p.Stream(context.Background(), r, func(Chunk) error { return nil }); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestOllamaContextWindowWireFormat(t *testing.T) {
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options struct {
				NumContext int64 `json:"num_ctx"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Options.NumContext != 131072 {
			t.Fatalf("Ollama context window missing: %+v, %v", body, err)
		}
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop"}`)
	})
	r := request()
	r.ContextTokens = 131072
	if err := p.Stream(context.Background(), r, func(Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidOutputTokenCeilingNeverSent(t *testing.T) {
	for _, kind := range []string{"openai_compatible", "ollama"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			p := fixtureProvider(t, kind, func(http.ResponseWriter, *http.Request) { requests++ })
			for _, limit := range []int64{-1, MaxOutputTokens + 1} {
				r := request()
				r.MaxOutputTokens = limit
				err := p.Stream(context.Background(), r, func(Chunk) error { return nil })
				var failure *Failure
				if !errors.As(err, &failure) || failure.Code != "invalid_request" {
					t.Fatalf("limit %d returned %v", limit, err)
				}
			}
			if requests != 0 {
				t.Fatalf("sent %d invalid requests", requests)
			}
		})
	}
}

func TestDiscovery(t *testing.T) {
	for _, kind := range []string{"ollama", "openai_compatible"} {
		t.Run(kind, func(t *testing.T) {
			p := fixtureProvider(t, kind, func(w http.ResponseWriter, r *http.Request) {
				if kind == "ollama" {
					if r.URL.Path != "/api/tags" {
						t.Error(r.URL.Path)
					}
					fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
				} else {
					if r.URL.Path != "/models" {
						t.Error(r.URL.Path)
					}
					fmt.Fprint(w, `{"data":[{"id":"fixture"}]}`)
				}
			})
			models, err := p.Models(context.Background())
			if err != nil || len(models) != 1 || models[0] != "fixture" {
				t.Fatalf("%v %v", models, err)
			}
		})
	}
}

func TestFailuresAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		retry  bool
	}{{400, "invalid_request", false}, {401, "authentication", false}, {413, "context_overflow", false}, {429, "rate_limit", true}, {503, "unavailable", true}, {302, "http_error", false}} {
		t.Run(tc.code+fmt.Sprint(tc.status), func(t *testing.T) {
			p := fixtureProvider(t, "openai_compatible", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/secret")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "fixture-secret")
			})
			err := p.Stream(context.Background(), request(), func(Chunk) error { return nil })
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Retryable != tc.retry || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected failure %v", err)
			}
		})
	}
}

func TestOpenAIContextOverflowCodeIsNormalizedWithoutDetailLeakage(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"context_length_exceeded","message":"fixture-secret"}}`,
		`{"error":{"code":"other","message":"fixture-secret"}}`,
		`{"error":{"code":"context_length_exceeded","message":"` + strings.Repeat("x", 4096) + `"}}`,
	} {
		p := fixtureProvider(t, "openai_compatible", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, body)
		})
		err := p.Stream(context.Background(), request(), func(Chunk) error {
			t.Error("error response emitted a chunk")
			return nil
		})
		var failure *Failure
		want := "context_overflow"
		if !strings.Contains(body, `"code":"context_length_exceeded"`) || len(body) > 4096 {
			want = "invalid_request"
		}
		if !errors.As(err, &failure) || failure.Code != want || failure.Retryable || failure.Partial || strings.Contains(err.Error(), "secret") {
			t.Fatalf("body classified unsafely: code=%q err=%v", want, err)
		}
	}
}

func TestCallbackAndCancellation(t *testing.T) {
	p := fixtureProvider(t, "openai_compatible", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	want := errors.New("consumer stopped")
	if err := p.Stream(context.Background(), request(), func(Chunk) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Stream(ctx, request(), func(Chunk) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHTTPConfiguredTimeoutBoundsUnresponsiveProvider(t *testing.T) {
	started := make(chan struct{})
	fixture := fixtureProvider(t, "openai_compatible", func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
	})
	p, err := NewHTTPWithTimeout(fixture.base, "openai_compatible", "", fixture.client.Transport, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	err = p.Stream(context.Background(), request(), func(Chunk) error { return nil })
	<-started
	var failure *Failure
	elapsed := time.Since(begin)
	if !errors.As(err, &failure) || failure.Code != "transport" || !failure.Retryable || elapsed < 50*time.Millisecond || elapsed > time.Second {
		t.Fatalf("timeout result err=%v elapsed=%v", err, elapsed)
	}
}

func TestInvalidRequestNeverSent(t *testing.T) {
	p := fixtureProvider(t, "openai_compatible", func(http.ResponseWriter, *http.Request) { t.Error("invalid request sent") })
	for _, r := range []Request{{}, {Model: "fixture", Messages: []Message{{Role: "invalid"}}}, {Model: "fixture", Messages: request().Messages, Tools: []Tool{{Name: "bad", Parameters: json.RawMessage(`null`)}}}} {
		if err := p.Stream(context.Background(), r, func(Chunk) error { return nil }); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
	if _, err := NewHTTP("https://user:secret@example.com", "ollama", "", http.DefaultTransport); err == nil {
		t.Fatal("credentials in endpoint accepted")
	}
	if _, err := NewHTTP("http://localhost", "ollama", "", nil); err == nil {
		t.Fatal("implicit transport accepted")
	}
}
