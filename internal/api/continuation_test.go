package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func continuationFixture() sessions.ContinuationStatus {
	return sessions.ContinuationStatus{Version: 1, TaskID: "task", Sequence: 6, State: "failed", HistoryEligible: true, Reason: "recovered_delegation"}
}

func continuationRequest(method, path, body string) *http.Request {
	r := request(method, path, body)
	if body == "" {
		r.Body = http.NoBody
	}
	return r
}

func TestTaskContinuationMetadataOnly(t *testing.T) {
	s := services()
	calls := 0
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("inspection executed task")
		return app.Result{}, nil
	}
	s.Inspect = func(context.Context, string) (sessions.Snapshot, error) {
		t.Fatal("inspection loaded public snapshot")
		return sessions.Snapshot{}, nil
	}
	s.TaskContinuation = func(ctx context.Context, task string) (sessions.ContinuationStatus, error) {
		calls++
		if task != "task" {
			t.Fatal(task)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Fatal("missing bounded deadline")
		}
		return continuationFixture(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/continuation", ""))
	var got sessions.ContinuationStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != continuationFixture() || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	var fields map[string]any
	if json.Unmarshal(w.Body.Bytes(), &fields) != nil || len(fields) != 6 {
		t.Fatal(w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable diagnostic")
	}
}

func TestTaskContinuationAdmission(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "bare_query", "body", "unknown_length", "transfer", "hidden_body", "method", "invalid_id", "missing_hook", "capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			calls := 0
			s.TaskContinuation = func(context.Context, string) (sessions.ContinuationStatus, error) {
				calls++
				return continuationFixture(), nil
			}
			if mode == "missing_hook" {
				s.TaskContinuation = nil
			}
			h, _ := New(token, 1, s)
			r := continuationRequest("GET", "/v1/tasks/task/continuation", "")
			want := 400
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = 401
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = 403
			case "query":
				r.URL.RawQuery = "secret=value"
			case "bare_query":
				r.URL.ForceQuery = true
			case "body":
				r = continuationRequest("GET", "/v1/tasks/task/continuation", "{}")
			case "unknown_length":
				r.ContentLength = -1
			case "transfer":
				r.TransferEncoding = []string{"chunked"}
			case "hidden_body":
				r.Body = io.NopCloser(strings.NewReader("private"))
				r.ContentLength = 0
			case "method":
				r.Method = "POST"
				want = 405
			case "invalid_id":
				r.URL.Path = "/v1/tasks/task/extra/continuation"
			case "missing_hook":
				want = 503
			case "capacity":
				h.controls <- struct{}{}
				h.controls <- struct{}{}
				want = 503
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = 503
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || calls != 0 {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestTaskContinuationBackendValidationAndErrors(t *testing.T) {
	for _, mode := range []string{"missing", "error", "panic", "version", "task", "sequence", "state", "reason", "eligibility", "canceled_context"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.TaskContinuation = func(context.Context, string) (sessions.ContinuationStatus, error) {
				out := continuationFixture()
				switch mode {
				case "missing":
					return out, sql.ErrNoRows
				case "error":
					return out, errors.New("private backend payload")
				case "panic":
					panic("private backend payload")
				case "version":
					out.Version = 2
				case "task":
					out.TaskID = "private backend payload"
				case "sequence":
					out.Sequence = 10001
				case "state":
					out.State = "private backend payload"
				case "reason":
					out.Reason = "private backend payload"
				case "eligibility":
					out.HistoryEligible = false
				case "canceled_context":
					cancel()
				}
				return out, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, continuationRequest("GET", "/v1/tasks/task/continuation", "").WithContext(ctx))
			want := 500
			if mode == "missing" {
				want = 404
			}
			if mode == "canceled_context" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "task_id") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskContinuationReadOnlyMethods(t *testing.T) {
	s := services()
	calls := 0
	s.TaskContinuation = func(context.Context, string) (sessions.ContinuationStatus, error) {
		calls++
		return continuationFixture(), nil
	}
	h, _ := New(token, 1, s)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, continuationRequest(method, "/v1/tasks/task/continuation", ""))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatal(method, w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("mutating method reached hook")
	}
}
