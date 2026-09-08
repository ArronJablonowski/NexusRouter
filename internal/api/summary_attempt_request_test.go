package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func TestNativeStoredSummaryAttemptForwarding(t *testing.T) {
	s := services()
	calls := 0
	s.Run = func(_ context.Context, got app.Request) (app.Result, error) {
		calls++
		if got.SummaryAttemptID != "approved-draft" || got.ContinueTaskID != "source" || got.Compaction != nil {
			t.Fatalf("unexpected request=%+v", got)
		}
		return app.Result{TaskID: "continued"}, nil
	}
	s.RunSubmission = fixedIdempotent(s.Run)
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"continue","continue_task_id":"source","summary_attempt_id":"approved-draft"}`))
	if w.Code != 201 || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
}

func TestNativeStoredSummaryAttemptStrictValidation(t *testing.T) {
	for _, fields := range []string{
		`"summary_attempt_id":null`, `"summary_attempt_id":1`, `"summary_attempt_id":true`, `"summary_attempt_id":[]`,
		`"summary_attempt_id":""`, `"summary_attempt_id":" "`, `"summary_attempt_id":" draft"`,
		`"summary_attempt_id":"draft\u0000id"`,
		`"summary_attempt_id":"` + strings.Repeat("a", 129) + `"`,
		`"summary_attempt_id":"draft","summary_attempt_id":"other"`,
		`"Summary_attempt_id":"draft"`,
		`"summary_attempt_id":"draft","compaction":{"keep":1,"summary":{"requirements":["retain"]}}`,
		`"compaction":{"keep":1,"summary":{"requirements":["retain"]}},"summary_attempt_id":"draft"`,
	} {
		t.Run(fields, func(t *testing.T) {
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("invalid native extension dispatched")
				return app.Result{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"auto","prompt":"continue","continue_task_id":"source",`+fields+`}`))
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	for _, body := range []string{
		`{"model_id":"auto","prompt":"continue","summary_attempt_id":"draft"}`,
		`{"model_id":"auto","prompt":"continue","continue_task_id":"","summary_attempt_id":"draft"}`,
	} {
		if _, err := decodeRequest(strings.NewReader(body)); err == nil {
			t.Fatal("missing source accepted")
		}
	}
	if _, err := decodeRequest(strings.NewReader(`{"model_id":"auto","prompt":"continue","continue_task_id":"source","summary_attempt_id":"` + strings.Repeat("a", 128) + `"}`)); err != nil {
		t.Fatal("maximum ID length rejected", err)
	}
}

func TestOpenAIRejectsStoredSummaryAttemptExtension(t *testing.T) {
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("native extension dispatched on OpenAI endpoint")
		return app.Result{}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/chat/completions", `{"model":"auto","messages":[{"role":"user","content":"continue"}],"summary_attempt_id":"draft"}`))
	if w.Code != 400 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
