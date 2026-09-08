package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
)

func usageFixture(task string) accounting.Totals {
	return accounting.Totals{
		Version:      1,
		Scope:        accounting.Scope{TaskID: task, SessionID: "session"},
		Coverage:     accounting.CompleteCoverage,
		CalculatedAt: time.Unix(100, 0).UTC(),
	}
}

func usageRequest(method, path string) *http.Request {
	r := request(method, path, "")
	r.Body = http.NoBody
	return r
}

func TestTaskUsageHTTPReturnsValidatedTotals(t *testing.T) {
	s := services()
	calls := 0
	s.TaskUsage = func(_ context.Context, task string) (accounting.Totals, error) {
		calls++
		return usageFixture(task), nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, usageRequest(http.MethodGet, "/v1/tasks/task/usage"))
	var got accounting.Totals
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.Scope.TaskID != "task" || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
}

func TestTaskUsageHTTPRejectsBeforeDispatch(t *testing.T) {
	s := services()
	calls := 0
	s.TaskUsage = func(context.Context, string) (accounting.Totals, error) {
		calls++
		return usageFixture("task"), nil
	}
	h, _ := New(token, 1, s)
	cases := []struct {
		name, method, path string
		mutate             func(*http.Request)
		status             int
	}{
		{"method", http.MethodPost, "/v1/tasks/task/usage", nil, http.StatusMethodNotAllowed},
		{"bad task", http.MethodGet, "/v1/tasks/bad%20task/usage", nil, http.StatusBadRequest},
		{"extra path", http.MethodGet, "/v1/tasks/task/extra/usage", nil, http.StatusBadRequest},
		{"query", http.MethodGet, "/v1/tasks/task/usage?x=1", nil, http.StatusBadRequest},
		{"force query", http.MethodGet, "/v1/tasks/task/usage?", nil, http.StatusBadRequest},
		{"body", http.MethodGet, "/v1/tasks/task/usage", func(r *http.Request) { r.Body = http.NoBody; r.ContentLength = 1 }, http.StatusBadRequest},
		{"transfer encoding", http.MethodGet, "/v1/tasks/task/usage", func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }, http.StatusBadRequest},
		{"unauthorized", http.MethodGet, "/v1/tasks/task/usage", func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"origin", http.MethodGet, "/v1/tasks/task/usage", func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			r := usageRequest(tc.method, tc.path)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || calls != before {
				t.Fatal(w.Code, w.Body.String(), calls, before)
			}
		})
	}
}

func TestTaskUsageHTTPFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		result accounting.Totals
		err    error
		panic  bool
		status int
	}{
		{name: "missing service", status: http.StatusServiceUnavailable},
		{name: "missing task", err: sql.ErrNoRows, status: http.StatusNotFound},
		{name: "backend", err: errors.New("private backend failure"), status: http.StatusInternalServerError},
		{name: "panic", panic: true, status: http.StatusInternalServerError},
		{name: "invalid", result: accounting.Totals{Version: 1}, status: http.StatusInternalServerError},
		{name: "wrong task", result: usageFixture("other"), status: http.StatusInternalServerError},
		{name: "missing session", result: usageFixture("task"), status: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.name != "missing service" {
				result := tc.result
				if tc.name == "missing session" {
					result.Scope.SessionID = ""
				}
				s.TaskUsage = func(context.Context, string) (accounting.Totals, error) {
					if tc.panic {
						panic("private panic secret")
					}
					return result, tc.err
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, usageRequest(http.MethodGet, "/v1/tasks/task/usage"))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskUsageHTTPRejectsSecretCollisionAndCapacity(t *testing.T) {
	s := services()
	calls := 0
	s.TaskUsage = func(_ context.Context, task string) (accounting.Totals, error) {
		calls++
		return usageFixture(task), nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, usageRequest(http.MethodGet, "/v1/tasks/"+token+"/usage"))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), token) || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, usageRequest(http.MethodGet, "/v1/tasks/task/usage"))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
}
