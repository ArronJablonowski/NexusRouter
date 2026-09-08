package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func TestNativeValidationRequest(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{"", ""}, {`,"validation":""`, ""}, {`,"validation":"go_source"`, "go_source"},
	} {
		s := services()
		calls := 0
		s.Run = func(_ context.Context, got app.Request) (app.Result, error) {
			calls++
			if got.Validation != tc.want {
				t.Fatalf("validation=%q want %q", got.Validation, tc.want)
			}
			return app.Result{TaskID: "task"}, nil
		}
		s.RunSubmission = fixedIdempotent(s.Run)
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello"`+tc.field+`}`))
		if w.Code != 201 || calls != 1 {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
		}
	}
}

func TestNativeValidationStrictEnum(t *testing.T) {
	for _, field := range []string{
		`"validation":null`, `"validation":true`, `"validation":1`, `"validation":[]`, `"validation":{}`,
		`"validation":"go"`, `"validation":"Go_source"`, `"validation":"go_source "`,
		`"validation":"go_source","validation":""`, `"Validation":"go_source"`,
	} {
		t.Run(field, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("invalid validation reached application")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello",`+field+`}`))
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestValidationRemainsUnsupportedOnOpenAIEndpoint(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("unsupported OpenAI request reached application")
		return app.Result{}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hello"}],"validation":"go_source"}`))
	if w.Code != 400 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
