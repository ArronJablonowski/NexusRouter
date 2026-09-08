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

	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func routeExplanationFixture(t *testing.T) sessions.RouteExplanation {
	t.Helper()
	policy := routing.Defaults()
	policy.Exploration = 0
	candidates := []routing.Candidate{{Model: "fixture", Provider: "local", Local: true, Capabilities: []string{"chat"}, ContextTokens: 8192, Healthy: true, PolicyAllowed: true, CapacityAvailable: true}}
	selection, err := routing.Select(routing.Request{Mode: "local_only", Domain: "code", Profile: "default", Capabilities: []string{"chat"}, ContextTokens: 10, MaxCost: 1}, policy, candidates, nil, time.Unix(100, 0), .5)
	if err != nil {
		t.Fatal(err)
	}
	return sessions.RouteExplanation{Version: 1, TaskID: "task", SessionID: "session", RouteID: "route", Sequence: 2, RecordedAt: time.Unix(101, 0), ConfigID: strings.Repeat("a", 64), Domain: "code", Profile: "default", Model: "fixture", Provider: "local", Candidates: candidates, Policy: policy, Selection: selection}
}

func routeRequest(method, path string) *http.Request {
	r := request(method, path, "")
	r.Body = http.NoBody
	return r
}

func TestRouteExplanationHTTPIsMetadataOnly(t *testing.T) {
	explanation := routeExplanationFixture(t)
	usage := usageFixture("task")
	usage.Scope.SessionID = explanation.SessionID
	usage.CalculatedAt = explanation.RecordedAt.Add(time.Second).UTC()
	explanation.Usage = &usage
	s := services()
	calls := 0
	s.RouteExplanation = func(_ context.Context, task string) (sessions.RouteExplanation, error) {
		calls++
		if task != "task" {
			t.Fatal(task)
		}
		return explanation, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, routeRequest(http.MethodGet, "/v1/tasks/task/route"))
	var got sessions.RouteExplanation
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Validate() != nil || got.TaskID != "task" || got.Usage == nil || got.Usage.Scope.TaskID != "task" || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	for _, private := range []string{"private prompt", "private output", "https://endpoint.invalid", "api-secret"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private value leaked", private)
		}
	}

	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/v1/tasks/task/route", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/tasks/task/route?x=1", http.StatusBadRequest},
		{http.MethodGet, "/v1/tasks/bad%20id/route", http.StatusBadRequest},
	} {
		before := calls
		w := httptest.NewRecorder()
		h.ServeHTTP(w, routeRequest(tc.method, tc.path))
		if w.Code != tc.status || calls != before {
			t.Fatal(w.Code, w.Body.String(), calls, before)
		}
	}
}

func TestRouteExplanationHTTPBackendFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result sessions.RouteExplanation
		err    error
		panic  bool
		status int
	}{
		{name: "missing service", status: http.StatusServiceUnavailable},
		{name: "missing route", err: sessions.ErrRouteExplanation, status: http.StatusNotFound},
		{name: "missing task", err: sql.ErrNoRows, status: http.StatusNotFound},
		{name: "backend", err: errors.New("private backend error"), status: http.StatusInternalServerError},
		{name: "invalid", result: sessions.RouteExplanation{Version: 1, TaskID: "task"}, status: http.StatusInternalServerError},
		{name: "mismatch", result: func() sessions.RouteExplanation { r := routeExplanationFixture(t); r.TaskID = "other"; return r }(), status: http.StatusInternalServerError},
		{name: "usage mismatch", result: func() sessions.RouteExplanation {
			r := routeExplanationFixture(t)
			u := usageFixture("other")
			r.Usage = &u
			return r
		}(), status: http.StatusInternalServerError},
		{name: "panic", panic: true, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.name != "missing service" {
				s.RouteExplanation = func(context.Context, string) (sessions.RouteExplanation, error) {
					if tc.panic {
						panic("private panic detail")
					}
					return tc.result, tc.err
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, routeRequest(http.MethodGet, "/v1/tasks/task/route"))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") || tc.status >= 500 && !strings.Contains(w.Body.String(), "route") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
