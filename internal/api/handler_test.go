package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

const token = "fixture-token-at-least-32-characters"

func services() Services {
	s := Services{Run: func(context.Context, app.Request) (app.Result, error) {
		return app.Result{TaskID: "task", Text: "answer", Turns: 1}, nil
	}, Inspect: func(context.Context, string) (sessions.Snapshot, error) {
		return sessions.Snapshot{TaskID: "task", State: "completed"}, nil
	}, Health: func(context.Context) error { return nil }}
	s.RunSubmission = fixedIdempotent(s.Run)
	return s
}
func fixedIdempotent(run func(context.Context, app.Request) (app.Result, error)) func(context.Context, string, app.Request) (submissions.Status, error) {
	return func(ctx context.Context, _ string, request app.Request) (submissions.Status, error) {
		result, err := run(ctx, request)
		if err != nil {
			return submissions.Status{}, err
		}
		if result.Turns == 0 {
			result.Turns = 1
		}
		now := time.Unix(100, 0).UTC()
		return submissions.Status{Version: 1, ID: "submission", State: "succeeded", CreatedAt: now, UpdatedAt: now, ConfigDigest: strings.Repeat("a", 64), TaskIDs: []string{result.TaskID}, Result: &submissions.Result{TaskID: result.TaskID, Text: result.Text, Turns: result.Turns, FinishReason: result.FinishReason, AuditID: result.AuditID, AuditStatus: result.AuditStatus, PreviousTaskIDs: result.PreviousTaskIDs, RouteEstimatedCost: result.RouteEstimatedCost, Usage: result.Usage}}, nil
	}
}
func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost && path == "/v1/tasks" {
		r.Header.Set("Idempotency-Key", "fixture-task-request")
	}
	return r
}
func TestAPIAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"run", "POST", "/v1/tasks", `{"model_id":"m","prompt":"hello"}`, 201},
		{"health", "GET", "/health", "", 200}, {"inspect", "GET", "/v1/tasks/task", "", 200},
		{"duplicate", "POST", "/v1/tasks", `{"model_id":"m","model_id":"n","prompt":"hello"}`, 400},
		{"unknown", "POST", "/v1/tasks", `{"model_id":"m","prompt":"hello","tools":[]}`, 400},
		{"null", "POST", "/v1/tasks", `{"model_id":"m","prompt":null}`, 400},
		{"trailing", "POST", "/v1/tasks", `{"model_id":"m","prompt":"hello"}{}`, 400},
		{"oversize", "POST", "/v1/tasks", `{"model_id":"m","prompt":"` + strings.Repeat("a", 1<<20) + `"}`, 400},
		{"query", "GET", "/health?token=secret", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := services()
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				calls++
				return app.Result{TaskID: "task", Turns: 1}, nil
			}
			s.RunSubmission = fixedIdempotent(s.Run)
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(tc.method, tc.path, tc.body))
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status >= 400 && calls != 0 {
				t.Fatal("invalid request dispatched")
			}
		})
	}
}

func TestNativeTaskResultIncludesRouteEstimatedCost(t *testing.T) {
	cost := .75
	s := services()
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		return app.Result{TaskID: "task", Text: "answer", Turns: 1, RouteEstimatedCost: &cost}, nil
	}
	s.RunSubmission = fixedIdempotent(s.Run)
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/v1/tasks", `{"model_id":"m","prompt":"hello"}`))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"route_estimated_cost":0.75`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestAuthenticationOriginsAndSafeErrors(t *testing.T) {
	for _, name := range []string{"unauthorized", "origin", "error", "panic"} {
		t.Run(name, func(t *testing.T) {
			s := services()
			calls := 0
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				calls++
				if name == "panic" {
					panic("private secret")
				}
				return app.Result{}, errors.New("private secret")
			}
			s.RunSubmission = fixedIdempotent(s.Run)
			h, _ := New(token, 1, s)
			r := request("POST", "/v1/tasks", `{"model_id":"m","prompt":"hi"}`)
			want := 500
			if name == "unauthorized" {
				r.Header.Del("Authorization")
				want = 401
			}
			if name == "origin" {
				r.Header.Set("Origin", "https://example.com")
				want = 403
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
			if want < 500 && calls != 0 {
				t.Fatal("denied request executed")
			}
		})
	}
}
func TestConcurrencyAndCancellation(t *testing.T) {
	s := services()
	started := make(chan struct{})
	s.Run = func(ctx context.Context, _ app.Request) (app.Result, error) {
		close(started)
		<-ctx.Done()
		return app.Result{}, ctx.Err()
	}
	s.RunSubmission = fixedIdempotent(s.Run)
	h, _ := New(token, 1, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := request("POST", "/v1/tasks", `{"model_id":"m","prompt":"hi"}`).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
	<-started
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("POST", "/v1/tasks", `{"model_id":"m","prompt":"hi"}`))
	if w.Code != 503 {
		t.Fatal("concurrency limit ignored", w.Code)
	}
	cancel()
	<-done
}
