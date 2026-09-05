package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/providers"
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

func TestChatBufferedSSE(t *testing.T) {
	w := httptest.NewRecorder()
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		if w.Body.Len() != 0 || w.Flushed {
			t.Fatal("stream began before durable completion")
		}
		return app.Result{Text: "Hello\n🌍"}, nil
	}
	h, _ := New(token, 1, s)
	h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-Darwin-Stream-Mode") != "buffered" || !w.Flushed {
		t.Fatal(w.Code, w.Header())
	}
	events := strings.Split(strings.TrimSuffix(w.Body.String(), "\n\n"), "\n\n")
	if len(events) != 3 || events[2] != "data: [DONE]" {
		t.Fatal(w.Body.String())
	}
	var previous map[string]any
	for i, event := range events[:2] {
		var body map[string]any
		if !strings.HasPrefix(event, "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(event, "data: ")), &body) != nil {
			t.Fatal(event)
		}
		if body["object"] != "chat.completion.chunk" || body["model"] != "m" {
			t.Fatal(body)
		}
		if i == 1 && (body["id"] != previous["id"] || body["created"] != previous["created"]) {
			t.Fatal("chunk identity changed")
		}
		choice := body["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if i == 0 && (delta["role"] != "assistant" || delta["content"] != "Hello\n🌍") {
			t.Fatal(delta)
		}
		if i == 1 && len(delta) != 0 {
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
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				return app.Result{Text: "private partial"}, tc.err
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.serveChatCompletions(w, request("POST", "/v1/chat/completions", strings.TrimSuffix(chatFixture, "}")+`,"stream":true}`))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "[DONE]") {
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
