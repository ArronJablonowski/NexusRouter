package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func healthReportFixture() health.Report {
	checks := []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", ID: "host", Status: "healthy", Code: "capacity_available"},
		{Component: "model", ID: "chat", Status: "healthy", Code: "available"},
	}
	status, ready := health.Outcome(checks)
	return health.Report{Version: 1, CheckedAt: time.Now().UTC(), Status: status, Ready: ready, Checks: checks}
}

func TestHealthReportReturnsValidatedReadiness(t *testing.T) {
	for _, state := range []string{"healthy", "degraded", "unavailable", "disabled", "unknown"} {
		t.Run(state, func(t *testing.T) {
			report := healthReportFixture()
			report.Checks[4].Status = state
			report.Checks[4].Code = map[string]string{"healthy": "available", "degraded": "capacity_exhausted", "unavailable": "model_missing", "disabled": "disabled_by_policy", "unknown": "unavailable"}[state]
			report.Status, report.Ready = health.Outcome(report.Checks)
			if err := report.Validate(); err != nil {
				t.Fatal(err)
			}
			s := services()
			s.HealthReport = func(context.Context) (health.Report, error) { return report, nil }
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("diagnostic executed a task")
				return app.Result{}, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			// Neither normal work nor control operations can starve health.
			h.slots <- struct{}{}
			h.controls <- struct{}{}
			h.controls <- struct{}{}
			h.intake <- struct{}{}
			h.intake <- struct{}{}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/health", ""))
			want := 503
			if report.Ready {
				want = 200
			}
			var got health.Report
			if w.Code != want || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, report) {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || len(h.healthSlots) != 0 {
				t.Fatal(w.Header(), len(h.healthSlots))
			}
		})
	}
	// An unknown supplemental metric is degraded, not a false readiness failure.
	report := healthReportFixture()
	report.Checks[3].Status, report.Checks[3].Code = "unknown", "metrics_unknown"
	report.Status, report.Ready = health.Outcome(report.Checks)
	s := services()
	s.HealthReport = func(context.Context) (health.Report, error) { return report, nil }
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/health", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"degraded"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHealthReportRejectsInvalidHookMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*health.Report){
		"version":             func(r *health.Report) { r.Version = 2 },
		"time":                func(r *health.Report) { r.CheckedAt = time.Time{} },
		"readiness":           func(r *health.Report) { r.Ready = false },
		"status":              func(r *health.Report) { r.Status = "unavailable" },
		"missing":             func(r *health.Report) { r.Checks = r.Checks[1:] },
		"duplicate":           func(r *health.Report) { r.Checks = append(r.Checks, r.Checks[0]) },
		"exception":           func(r *health.Report) { r.Checks[0].Code = "private provider exception" },
		"contradictory check": func(r *health.Report) { r.Checks[4].Code = "unavailable" },
		"id injection":        func(r *health.Report) { r.Checks[4].ID = "private\nheader" },
		"oversize":            func(r *health.Report) { r.Checks = make([]health.Check, 513) },
	} {
		t.Run(name, func(t *testing.T) {
			r := healthReportFixture()
			mutate(&r)
			s := services()
			s.HealthReport = func(context.Context) (health.Report, error) { return r, nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/health", ""))
			if w.Code != 503 || w.Body.String() != "{\"error\":\"health_unavailable\"}\n" {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestHealthReportConcurrentCapacityIsIndependent(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := services()
	s.HealthReport = func(context.Context) (health.Report, error) {
		close(entered)
		<-release
		return healthReportFixture(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	go func() { defer close(done); h.ServeHTTP(w, request("GET", "/v1/health", "")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook not entered")
	}
	other := httptest.NewRecorder()
	h.ServeHTTP(other, request("GET", "/v1/health", ""))
	if other.Code != 503 || other.Header().Get("Retry-After") != "1" {
		t.Error(other.Code, other.Body.String())
	}
	if len(h.controls) != 0 || len(h.slots) != 0 || len(h.intake) != 0 {
		t.Error("health borrowed execution/control capacity")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hook did not finish")
	}
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHealthReportAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		mutate                   func(*Handler, *http.Request)
		status                   int
	}{
		{name: "authentication", method: "GET", path: "/v1/health", mutate: func(_ *Handler, r *http.Request) { r.Header.Del("Authorization") }, status: 401},
		{name: "origin", method: "GET", path: "/v1/health", mutate: func(_ *Handler, r *http.Request) { r.Header.Set("Origin", "https://example.test") }, status: 403},
		{name: "query", method: "GET", path: "/v1/health?token=private", status: 400},
		{name: "body", method: "GET", path: "/v1/health", body: `{}`, status: 400},
		{name: "method", method: "POST", path: "/v1/health", status: 404},
		{name: "suffix", method: "GET", path: "/v1/health/private", status: 404},
		{name: "missing hook", method: "GET", path: "/v1/health", mutate: func(h *Handler, _ *http.Request) { h.services.HealthReport = nil }, status: 503},
		{name: "capacity", method: "GET", path: "/v1/health", mutate: func(h *Handler, _ *http.Request) { h.healthSlots <- struct{}{} }, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			calls := 0
			s.HealthReport = func(context.Context) (health.Report, error) { calls++; return health.Report{}, nil }
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("health executed a task")
				return app.Result{}, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request(tc.method, tc.path, tc.body)
			if tc.mutate != nil {
				tc.mutate(h, r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || calls != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
			if tc.name == "capacity" && w.Header().Get("Retry-After") != "1" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestHealthReportFailuresAreGenericAndReleaseSlot(t *testing.T) {
	for _, kind := range []string{"error", "panic", "invalid", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			s := services()
			s.HealthReport = func(ctx context.Context) (health.Report, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 6*time.Second {
					t.Fatal("missing bounded diagnostic deadline")
				}
				if kind == "panic" {
					panic("private provider credentials")
				}
				if kind == "error" {
					return health.Report{}, errors.New("private provider credentials")
				}
				return health.Report{Version: 1, Status: "private provider credentials"}, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request("GET", "/v1/health", "")
			if kind == "canceled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 503 || w.Body.String() != "{\"error\":\"health_unavailable\"}\n" || len(h.healthSlots) != 0 {
				t.Fatal(w.Code, w.Body.String(), len(h.healthSlots))
			}
		})
	}
}

func TestLegacyHealthRemainsIndependent(t *testing.T) {
	s := services()
	s.HealthReport = func(context.Context) (health.Report, error) {
		t.Fatal("legacy route invoked detailed checks")
		return health.Report{}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.healthSlots <- struct{}{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/health", ""))
	var got map[string]any
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != 200 || !reflect.DeepEqual(got, map[string]any{"status": "ok", "providers_checked": false}) {
		t.Fatal(w.Code, w.Body.String())
	}
}
