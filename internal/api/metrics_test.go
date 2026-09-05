package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
)

type metricsUnreadBody struct{ t *testing.T }

func (b metricsUnreadBody) Read([]byte) (int, error) {
	b.t.Error("metrics read a request body")
	return 0, errors.New("unexpected read")
}
func (b metricsUnreadBody) Close() error { return nil }

func TestMetricsAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		mutate             func(*Handler, *http.Request)
		status             int
	}{
		{"auth", "GET", "/v1/metrics", func(_ *Handler, r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"origin", "GET", "/v1/metrics", func(_ *Handler, r *http.Request) { r.Header.Set("Origin", "https://example.test") }, 403},
		{"query", "GET", "/v1/metrics?key=private", nil, 400},
		{"post", "POST", "/v1/metrics", nil, 404},
		{"suffix", "GET", "/v1/metrics/private", nil, 404},
		{"body", "GET", "/v1/metrics", func(_ *Handler, r *http.Request) { r.ContentLength = 1 }, 400},
		{"unknown body", "GET", "/v1/metrics", func(_ *Handler, r *http.Request) { r.ContentLength = -1 }, 400},
		{"chunked", "GET", "/v1/metrics", func(_ *Handler, r *http.Request) { r.TransferEncoding = []string{"chunked"} }, 400},
		{"missing", "GET", "/v1/metrics", func(h *Handler, _ *http.Request) { h.services.Metrics = nil }, 503},
		{"capacity", "GET", "/v1/metrics", func(h *Handler, _ *http.Request) { h.metricsSlots <- struct{}{} }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			calls := 0
			s.Metrics = func(context.Context) (metrics.Snapshot, error) { calls++; return metrics.Snapshot{}, nil }
			s.Run = func(context.Context, app.Request) (app.Result, error) {
				t.Fatal("metrics executed a task")
				return app.Result{}, nil
			}
			h, err := New(token, 1, s)
			if err != nil {
				t.Fatal(err)
			}
			r := request(tc.method, tc.path, "")
			r.Body = metricsUnreadBody{t}
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

func TestMetricsReturnsValidatedSnapshotIndependently(t *testing.T) {
	want := metrics.NewSnapshot(13, time.Now().UTC())
	if err := want.Validate(); err != nil {
		t.Fatal(err)
	}
	s := services()
	calls := 0
	s.Metrics = func(ctx context.Context) (metrics.Snapshot, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) < 4*time.Second {
			t.Error("metrics deadline not bounded to five seconds")
		}
		return want, nil
	}
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		t.Fatal("metrics executed a task")
		return app.Result{}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{}
	h.controls <- struct{}{}
	h.controls <- struct{}{}
	h.intake <- struct{}{}
	h.intake <- struct{}{}
	h.healthSlots <- struct{}{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/metrics", ""))
	var got metrics.Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) || calls != 1 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	if len(h.metricsSlots) != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
}

func TestMetricsHookFailuresAreGeneric(t *testing.T) {
	for _, kind := range []string{"error", "panic", "invalid", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			s := services()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.Metrics = func(context.Context) (metrics.Snapshot, error) {
				if kind == "panic" {
					panic("private database path")
				}
				if kind == "error" {
					return metrics.Snapshot{}, errors.New("private database path")
				}
				if kind == "canceled" {
					cancel()
					return metrics.NewSnapshot(13, time.Now().UTC()), nil
				}
				return metrics.Snapshot{}, nil
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/metrics", "").WithContext(ctx))
			if w.Code != 503 || w.Body.String() != "{\"error\":\"metrics_unavailable\"}\n" || len(h.metricsSlots) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestMetricsCanceledRequestDoesNotInvokeHook(t *testing.T) {
	s := services()
	s.Metrics = func(context.Context) (metrics.Snapshot, error) {
		t.Fatal("canceled diagnostic invoked hook")
		return metrics.Snapshot{}, nil
	}
	h, _ := New(token, 1, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", "/v1/metrics", "").WithContext(ctx))
	if w.Code != 503 || len(h.metricsSlots) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestMetricsRejectsInvalidHookSnapshot(t *testing.T) {
	for name, mutate := range map[string]func(*metrics.Snapshot){
		"version":      func(s *metrics.Snapshot) { s.Version = 2 },
		"time":         func(s *metrics.Snapshot) { s.ObservedAt = time.Time{} },
		"schema":       func(s *metrics.Snapshot) { s.StorageSchema = 15 },
		"secret group": func(s *metrics.Snapshot) { s.Groups[0].Name = "private-token" },
		"secret state": func(s *metrics.Snapshot) { s.Groups[0].Counts[0].State = "private-token" },
		"availability": func(s *metrics.Snapshot) { s.Groups[0].Available = false },
		"negative":     func(s *metrics.Snapshot) { s.Groups[0].Counts[0].Value = -1 },
		"overflow": func(s *metrics.Snapshot) {
			s.Groups[0].Counts[0].Value = math.MaxInt64
			s.Groups[0].Counts[1].Value = 1
		},
		"null counts": func(s *metrics.Snapshot) { s.Groups[0].Counts = nil },
		"extra group": func(s *metrics.Snapshot) { s.Groups = append(s.Groups, s.Groups[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := metrics.NewSnapshot(13, time.Now().UTC())
			mutate(&snapshot)
			s := services()
			s.Metrics = func(context.Context) (metrics.Snapshot, error) { return snapshot, nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request("GET", "/v1/metrics", ""))
			if w.Code != 503 || w.Body.String() != "{\"error\":\"metrics_unavailable\"}\n" {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestMetricsConcurrentCapacity(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := services()
	s.Metrics = func(context.Context) (metrics.Snapshot, error) {
		close(entered)
		<-release
		return metrics.NewSnapshot(13, time.Now().UTC()), nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	go func() { defer close(done); h.ServeHTTP(w, request("GET", "/v1/metrics", "")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook not entered")
	}
	other := httptest.NewRecorder()
	h.ServeHTTP(other, request("GET", "/v1/metrics", ""))
	if other.Code != 503 || other.Header().Get("Retry-After") != "1" {
		t.Error(other.Code, other.Body.String())
	}
	if len(h.healthSlots) != 0 || len(h.controls) != 0 || len(h.slots) != 0 || len(h.intake) != 0 {
		t.Error("metrics borrowed other capacity")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hook did not finish")
	}
	if w.Code != 200 || len(h.metricsSlots) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
