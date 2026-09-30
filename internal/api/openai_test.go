package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

const chatFixture = `{"model":"m","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"},{"role":"user","content":"你好\nagain"}]}`

func TestChatPreservesConversationAndCompletion(t *testing.T) {
	s := services()
	s.Run = func(_ context.Context, req app.Request) (app.Result, error) {
		want := []providers.Message{{Role: "system", Content: "Be concise."}, {Role: "user", Content: "Hi"}, {Role: "assistant", Content: "Hello"}, {Role: "user", Content: "你好\nagain"}}
		if req.ModelID != "m" || req.Prompt != "" || !reflect.DeepEqual(req.Messages, want) {
			t.Fatalf("conversation changed: %#v", req)
		}
		return app.Result{TaskID: "task", Text: "你好\n[REDACTED]", Turns: 1}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", chatFixture))
	var body map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	if body["object"] != "chat.completion" || body["model"] != "m" || !strings.HasPrefix(body["id"].(string), "chatcmpl-") || body["created"].(float64) <= 0 || choice["finish_reason"] != nil {
		t.Fatal(body)
	}
	if _, ok := body["usage"]; ok {
		t.Fatal("invented usage", body)
	}
	message := choice["message"].(map[string]any)
	if message["role"] != "assistant" || message["content"] != "你好\n[REDACTED]" {
		t.Fatal(message)
	}
}

func TestChatStrictSubset(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"model":null,"messages":[]}`, `{"Model":"m","messages":[]}`,
		`{"model":"m","model":"n","messages":[]}`,
		`{"model":"m","messages":null}`, `{"model":"m","messages":[]}`,
		`{"model":"m","messages":[{"role":"user","role":"system","content":"x"}]}`,
		`{"model":"m","messages":[{"role":"user","content":null}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`,
		`{"model":"m","messages":[{"role":"tool","content":"x"}]}`,
		`{"model":"m","messages":[{"role":"developer","content":"x"}]}`,
		`{"model":"m","messages":[{"role":"assistant","content":"x","tool_calls":[]}]}`,
		`{"model":"m","messages":[{"role":"user","content":"x","name":"n"}]}`,
		chatFixture + `{}`,
		strings.TrimSuffix(chatFixture, "}") + `,"stream":null}`,
		strings.TrimSuffix(chatFixture, "}") + `,"stream":"true"}`,
		strings.TrimSuffix(chatFixture, "}") + `,"tools":[]}`,
		strings.TrimSuffix(chatFixture, "}") + `,"temperature":0.2}`,
		strings.TrimSuffix(chatFixture, "}") + `,"response_format":{"type":"json_object"}}`,
		strings.TrimSuffix(chatFixture, "}") + `,"stream_options":{"include_usage":true}}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("invalid request dispatched")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.serveChatCompletions(w, request("POST", "/v1/chat/completions", body))
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestChatLiveSSE(t *testing.T) {
	w := httptest.NewRecorder()
	s := services()
	s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
		if w.Body.Len() == 0 || !w.Flushed {
			t.Fatal("stream role was not flushed before execution")
		}
		if err := emit("Hello\n🌍"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(w.Body.String(), "Hello") {
			t.Fatal("text held until completion")
		}
		return app.Result{Text: "Hello\n🌍"}, nil
	}
	h, _ := New(token, 1, s)
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-Darwin-Stream-Mode") != "live-redacted" || !w.Flushed {
		t.Fatal(w.Code, w.Header())
	}
	events := strings.Split(strings.TrimSuffix(w.Body.String(), "\n\n"), "\n\n")
	if len(events) != 4 || events[3] != "data: [DONE]" {
		t.Fatal(w.Body.String())
	}
	var previous map[string]any
	for i, event := range events[:3] {
		var body map[string]any
		if !strings.HasPrefix(event, "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(event, "data: ")), &body) != nil {
			t.Fatal(event)
		}
		if body["object"] != "chat.completion.chunk" || body["model"] != "m" {
			t.Fatal(body)
		}
		if i > 0 && (body["id"] != previous["id"] || body["created"] != previous["created"]) {
			t.Fatal("chunk identity changed")
		}
		choice := body["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if i == 0 && delta["role"] != "assistant" {
			t.Fatal(delta)
		}
		if i == 1 && delta["content"] != "Hello\n🌍" {
			t.Fatal(delta)
		}
		if i == 2 && len(delta) != 0 {
			t.Fatal(delta)
		}
		if choice["finish_reason"] != nil {
			t.Fatal("invented finish reason")
		}
		if _, ok := body["usage"]; ok {
			t.Fatal("invented usage")
		}
		previous = body
	}
}

func TestChatLimitsErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"failure", errors.New("private credentials"), 500}, {"admission", app.ErrAdmission, 422}, {"timeout", context.DeadlineExceeded, 504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(context.Context, app.Request, func(string) error) (app.Result, error) {
				return app.Result{Text: "private partial"}, tc.err
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	h, _ := New(token, 1, services())
	w := httptest.NewRecorder()
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.Repeat("x", (1<<20)+1)))
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	r := request("POST", "/v1/chat/completions", chatFixture)
	r.Header.Set("Content-Type", "text/plain")
	h.serveChatCompletions(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	many := `{"model":"m","messages":[` + strings.Repeat(`{"role":"user","content":"x"},`, 256) + `{"role":"user","content":"x"}]}`
	if _, _, err := decodeChatRequest([]byte(many)); err == nil {
		t.Fatal("unbounded message count")
	}
	h.slots <- struct{}{}
	w = httptest.NewRecorder()
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", chatFixture))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Header())
	}
	<-h.slots
	s := services()
	started := make(chan struct{})
	s.Run = func(ctx context.Context, _ app.Request) (app.Result, error) {
		close(started)
		<-ctx.Done()
		return app.Result{Text: "partial"}, ctx.Err()
	}
	h, _ = New(token, 1, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.serveChatCompletions(w, request("POST", "/v1/chat/completions", chatFixture).WithContext(ctx))
	}()
	<-started
	cancel()
	<-done
	if w.Body.Len() != 0 || len(h.slots) != 0 {
		t.Fatal("canceled response or leaked capacity", w.Body.String())
	}
}

func TestChatReportedMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s := services()
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			return app.Result{Text: "truncated", FinishReason: "length", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 3}}, nil
		}
		s.RunTextStream = func(ctx context.Context, req app.Request, emit func(string) error) (app.Result, error) {
			if err := emit("truncated"); err != nil {
				return app.Result{}, err
			}
			return s.Run(ctx, req)
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		body := chatFixture
		if stream {
			body = strings.TrimSuffix(body, "}") + `,"stream":true}`
		}
		h.serveChatCompletions(w, request("POST", "/v1/chat/completions", body))
		if !strings.Contains(w.Body.String(), `"finish_reason":"length"`) {
			t.Fatal(w.Body.String())
		}
		if !stream && !strings.Contains(w.Body.String(), `"usage":{"completion_tokens":3,"prompt_tokens":7,"total_tokens":10}`) {
			t.Fatal(w.Body.String())
		}
		if stream && strings.Contains(w.Body.String(), `"usage"`) {
			t.Fatal("unsolicited stream usage")
		}
	}
}

func TestChatSharedAuthentication(t *testing.T) {
	for _, name := range []string{"valid", "unauthorized", "origin", "query"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) { calls++; return app.Result{Text: "ok"}, nil }
			h, _ := New(token, 1, s)
			r := request("POST", "/v1/chat/completions", chatFixture)
			status := 200
			switch name {
			case "unauthorized":
				r.Header.Del("Authorization")
				status = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				status = 403
			case "query":
				r.URL.RawQuery = "token=secret"
				status = 400
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != status || (status != 200 && calls != 0) || (status == 200 && calls != 1) {
				t.Fatal(w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestChatStreamUnavailableDoesNotFallBack(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("stream request silently buffered")
		return app.Result{}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "streaming_unavailable") || len(h.slots) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestChatStreamDoesNotFinishAfterFailure(t *testing.T) {
	for _, mode := range []string{"provider", "panic", "invalid_utf8", "oversize", "invalid_result"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				if err := emit("provisional"); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "provider":
					return app.Result{Text: "private final"}, errors.New("private provider error")
				case "panic":
					panic("private panic")
				case "invalid_utf8":
					if emit(string([]byte{0xff})) == nil {
						t.Fatal("invalid text accepted")
					}
				case "oversize":
					if emit(strings.Repeat("x", 1<<20)) == nil {
						t.Fatal("aggregate limit ignored")
					}
				case "invalid_result":
					return app.Result{Text: string([]byte{0xff})}, nil
				}
				// Even a service that ignores delivery errors cannot fabricate
				// a successful end-of-stream marker.
				return app.Result{Text: "private final"}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(body, "provisional") || !strings.Contains(body, `"error"`) || strings.Contains(body, "private") || strings.Contains(body, "[DONE]") || len(h.slots) != 0 {
				t.Fatal(w.Code, body)
			}
		})
	}
}

type chatFailWriter struct {
	*httptest.ResponseRecorder
	writes int
	mode   string
}

func (w *chatFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		switch w.mode {
		case "panic":
			panic("private writer panic")
		case "short":
			return 0, nil
		case "write":
			return 0, errors.New("private write error")
		}
	}
	return w.ResponseRecorder.Write(p)
}

func (w *chatFailWriter) FlushError() error {
	if w.writes > 1 && w.mode == "flush" {
		return errors.New("private flush error")
	}
	w.ResponseRecorder.Flush()
	return nil
}

// Hide ResponseRecorder.WriteString so io.WriteString exercises Write.
type chatWriterOnly struct{ w *chatFailWriter }

func (w chatWriterOnly) Header() http.Header         { return w.w.Header() }
func (w chatWriterOnly) WriteHeader(code int)        { w.w.WriteHeader(code) }
func (w chatWriterOnly) Write(p []byte) (int, error) { return w.w.Write(p) }
func (w chatWriterOnly) FlushError() error           { return w.w.FlushError() }

func TestChatStreamWriterFailureStopsDelivery(t *testing.T) {
	for _, mode := range []string{"panic", "short", "write", "flush"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				if emit("provisional") == nil || emit("later") == nil {
					t.Fatal("delivery failure not sticky")
				}
				return app.Result{Text: "final"}, nil
			}
			h, _ := New(token, 1, s)
			w := &chatFailWriter{ResponseRecorder: httptest.NewRecorder(), mode: mode}
			h.serveChatCompletions(chatWriterOnly{w}, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
			if w.writes != 2 || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "private") || len(h.slots) != 0 {
				t.Fatal(w.writes, w.Body.String())
			}
		})
	}
}
