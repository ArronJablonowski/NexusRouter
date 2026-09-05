package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

func TestFeedbackRevisionStrictAndAuthenticated(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"task_id":"t","expected_id":"e","outcome":null}`,
		`{"task_id":"t","expected_id":"e","outcome":"accepted","attempt_cost":0}`,
		`{"task_id":"t","expected_id":"e","outcome":"accepted","outcome":"rejected"}`,
		`{"task_id":"t","expected_id":1,"outcome":"accepted"}`,
		`{"task_id":"t","expected_id":"e","outcome":"maybe"}`,
	} {
		s := services()
		s.ReviseFeedback = func(context.Context, string, string, bool) error { t.Fatal("invalid revision dispatched"); return nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/feedback/revisions", body))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	s := services()
	s.ReviseFeedback = func(context.Context, string, string, bool) error {
		t.Fatal("unauthenticated revision dispatched")
		return nil
	}
	h, _ := New(token, 1, s)
	r := request("POST", "/v1/feedback/revisions", `{"task_id":"t","expected_id":"e","outcome":"accepted"}`)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestFeedbackRevisionStatusAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{nil, 200}, {telemetry.ErrConflict, 409}, {app.ErrAdmission, 422}} {
		s := services()
		s.ReviseFeedback = func(ctx context.Context, task, expected string, accepted bool) error {
			if task != "task" || expected != "evaluation" || accepted {
				t.Fatal("fields lost")
			}
			return tc.err
		}
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/feedback/revisions", `{"task_id":"task","expected_id":"evaluation","outcome":"rejected"}`))
		if w.Code != tc.status {
			t.Fatal(w.Code)
		}
		h.slots <- struct{}{}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/feedback/revisions", `{}`))
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
		<-h.slots
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("POST", "/v1/feedback/revisions", strings.Repeat("x", 4097)))
		if w.Code != 413 {
			t.Fatal(w.Code)
		}
	}
}
