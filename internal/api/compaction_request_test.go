package api

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestNativeCompactionRequestForwarding(t *testing.T) {
	s := services()
	calls := 0
	want := &sessions.CompactionRequest{Keep: 3, Summary: sessions.Summary{Decisions: []string{"use Go"}, PendingWork: []string{"test"}, Failures: []string{"prior check failed"}, Artifacts: []string{"main.go"}}}
	s.Run = func(_ context.Context, got app.Request) (app.Result, error) {
		calls++
		if !reflect.DeepEqual(got.Compaction, want) || got.ContinueTaskID != "previous" {
			t.Fatalf("unexpected request %#v", got)
		}
		return app.Result{TaskID: "task"}, nil
	}
	s.RunSubmission = fixedIdempotent(s.Run)
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello","continue_task_id":"previous","compaction":{"keep":3,"summary":{"decisions":["use Go"],"pending_work":["test"],"failures":["prior check failed"],"artifacts":["main.go"]}}}`))
	if w.Code != 201 || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
}

func TestNativeCompactionStrictValidation(t *testing.T) {
	for _, value := range []string{
		`null`, `[]`, `{}`, `{"keep":1}`, `{"summary":{"decisions":["a"]}}`,
		`{"keep":0,"summary":{"decisions":["a"]}}`, `{"keep":-1,"summary":{"decisions":["a"]}}`,
		`{"keep":100001,"summary":{"decisions":["a"]}}`, `{"keep":1.5,"summary":{"decisions":["a"]}}`,
		`{"keep":"1","summary":{"decisions":["a"]}}`, `{"keep":null,"summary":{"decisions":["a"]}}`,
		`{"keep":1,"keep":2,"summary":{"decisions":["a"]}}`,
		`{"keep":1,"summary":{"decisions":["a"]},"unknown":"secret-value"}`,
		`{"keep":1,"summary":null}`, `{"keep":1,"summary":{}}`, `{"keep":1,"summary":{"decisions":null}}`,
		`{"keep":1,"summary":{"decisions":[null]}}`, `{"keep":1,"summary":{"decisions":[1]}}`,
		`{"keep":1,"summary":{"decisions":[" "]}}`, `{"keep":1,"summary":{"Decisions":["a"]}}`,
		`{"keep":1,"summary":{"decisions":["a"],"decisions":["b"]}}`,
		`{"keep":1,"summary":{"decisions":["a"],"unknown":[]}}`,
		`{"keep":1,"summary":{"decisions":["` + strings.Repeat("a", 64<<10) + `"]}}`,
	} {
		t.Run(value[:min(len(value), 100)], func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("invalid compaction dispatched")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"hello","continue_task_id":"previous","compaction":`+value+`}`))
			if w.Code != 400 || strings.Contains(w.Body.String(), "secret-value") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	for _, body := range []string{
		`{"model_id":"auto","prompt":"hello","compaction":{"keep":1,"summary":{"decisions":["a"]}}}`,
		`{"model_id":"auto","prompt":"hello","continue_task_id":"previous","compaction":{"keep":1,"summary":{"decisions":["a"]}},"compaction":{"keep":1,"summary":{"decisions":["a"]}}}`,
	} {
		if _, err := decodeRequest(strings.NewReader(body)); err == nil {
			t.Fatal("invalid compaction envelope accepted")
		}
	}
}

func TestOpenAIRejectsCompactionExtension(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("native extension dispatched on OpenAI endpoint")
		return app.Result{}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"hello"}],"compaction":{"keep":1,"summary":{"decisions":["a"]}}}`))
	if w.Code != 400 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
