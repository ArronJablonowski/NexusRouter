package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

func TestChatHarnessSelectionPreservesAuthorityBoundary(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s := services()
		calls := 0
		run := func(_ context.Context, req app.Request) (app.Result, error) {
			calls++
			if req.HarnessID != "pi-local" || req.ModelID != "m" || len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
				t.Fatal("lost selection or context", req)
			}
			return app.Result{TaskID: "task", Text: "answer", FinishReason: "stop"}, nil
		}
		s.Run = run
		s.RunTextStream = func(ctx context.Context, r app.Request, emit func(string) error) (app.Result, error) {
			result, err := run(ctx, r)
			if err == nil {
				err = emit(result.Text)
			}
			return result, err
		}
		h, err := New(token, 1, s)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"harness_id":"pi-local"`
		if stream {
			body += `,"stream":true`
		}
		body += "}"
		unauthorized := request("POST", "/v1/chat/completions", body)
		unauthorized.Header.Del("Authorization")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, unauthorized)
		if w.Code != 401 || calls != 0 {
			t.Fatal("unauthorized harness dispatched")
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/chat/completions", body))
		if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "answer") {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
	}
}

func TestChatHarnessRejectsInvalidAndExecutableInput(t *testing.T) {
	for _, field := range []string{`"harness_id":null`, `"harness_id":7`, `"harness_id":""`, `"harness_id":" x"`, `"harness_id":"x\n"`, `"harness_id":"x","harness_id":"y"`, `"Harness_ID":"x"`, `"executable":"/tmp/evil"`, `"native_harnesses":[]`} {
		s := services()
		s.Run = func(context.Context, app.Request) (app.Result, error) {
			t.Fatal("invalid input dispatched")
			return app.Result{}, nil
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}],`+field+`}`))
		if w.Code != 400 {
			t.Fatal(field, w.Code, w.Body.String())
		}
	}
}

func TestChatUnsupportedHarnessIsAdmissionFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s := services()
		s.Run = func(context.Context, app.Request) (app.Result, error) { return app.Result{}, app.ErrHarnessUnsupported }
		s.RunTextStream = func(context.Context, app.Request, func(string) error) (app.Result, error) {
			return app.Result{}, app.ErrHarnessUnsupported
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"harness_id":"unknown"`
		if stream {
			body += `,"stream":true`
		}
		body += "}"
		h.ServeHTTP(w, request("POST", "/v1/chat/completions", body))
		expectedStatus := 422
		if stream {
			expectedStatus = 200 // Streaming errors use an error frame after headers.
		}
		if w.Code != expectedStatus || !strings.Contains(w.Body.String(), "admission_denied") || strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
