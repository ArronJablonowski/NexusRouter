package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"darwinrouter/internal/app"
)

func TestNativeRoutingRequest(t *testing.T) {
	want := app.Request{ModelID: "auto", Prompt: "hello", Domain: "coding", Profile: "quality", Capabilities: []string{"chat", "tools"}, ContextTokens: 4096, MaxCost: .25, LocalRequired: true}
	s := services()
	calls := 0
	s.Run = func(_ context.Context, got app.Request) (app.Result, error) {
		calls++
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request = %#v, want %#v", got, want)
		}
		return app.Result{TaskID: "task"}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello","domain":"coding","profile":"quality","capabilities":["chat","tools"],"context_tokens":4096,"max_cost":0.25,"local_required":true}`))
	if w.Code != 201 || calls != 1 {
		t.Fatalf("status %d, calls %d: %s", w.Code, calls, w.Body.String())
	}
}

func TestNativeRoutingStrictValidation(t *testing.T) {
	for _, field := range []string{
		`"domain":null`, `"domain":1`, `"profile":false`, `"profile":null`,
		`"capabilities":null`, `"capabilities":"chat"`, `"capabilities":[null]`, `"capabilities":[1]`,
		`"context_tokens":null`, `"context_tokens":"1"`, `"context_tokens":true`, `"context_tokens":1.5`, `"context_tokens":-1`, `"context_tokens":9223372036854775808`,
		`"max_cost":null`, `"max_cost":"1"`, `"max_cost":false`, `"max_cost":-0.1`, `"max_cost":1e999`,
		`"local_required":null`, `"local_required":1`, `"local_required":"true"`,
		`"Domain":"coding"`, `"routing":{}`, `"domain":"a","domain":"b"`,
		`"profile":"a","profile":"b"`, `"capabilities":[],"capabilities":[]`,
		`"context_tokens":1,"context_tokens":2`, `"max_cost":0,"max_cost":1`, `"local_required":false,"local_required":true`,
	} {
		t.Run(field, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("invalid request dispatched")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello",`+field+`}`))
			if w.Code != 400 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, body := range []string{`{"prompt":"hello","domain":"coding"}`, `{"model_id":"","prompt":"hello"}`} {
		if _, err := decodeRequest(strings.NewReader(body)); err == nil {
			t.Fatalf("model_id requirement lost: %s", body)
		}
	}
	if _, err := decodeRequest(strings.NewReader(`{"model_id":"m","prompt":"hi","capabilities":[],"context_tokens":0,"max_cost":0,"local_required":false}`)); err != nil {
		t.Fatal("valid zero values rejected", err)
	}
}

func TestChatSharedFailureEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, kind, code string
		status           int
	}{
		{"auth", "authentication_error", "unauthorized", 401},
		{"origin", "permission_error", "browser_origin_denied", 403},
		{"query", "invalid_request_error", "query_not_supported", 400},
		{"method", "invalid_request_error", "not_found", 404},
		{"panic", "server_error", "internal_error", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) { panic("private secret") }
			h, _ := New(token, 1, s)
			r := request("POST", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
			switch tc.name {
			case "auth":
				r.Header.Del("Authorization")
			case "origin":
				r.Header.Set("Origin", "https://example.com")
			case "query":
				r.URL.RawQuery = "key=value"
			case "method":
				r.Method = "GET"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var body struct {
				Error struct {
					Message, Type, Code string
					Param               any
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Error.Type != tc.kind || body.Error.Code != tc.code || body.Error.Message != tc.code || body.Error.Param != nil {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
