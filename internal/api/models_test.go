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

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

func TestModelCatalogOpenAIShapeAndAdmission(t *testing.T) {
	s := services()
	calls := 0
	runs := 0
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		runs++
		return app.Result{}, errors.New("catalog must not execute")
	}
	s.Models = func(context.Context) ([]string, error) {
		calls++
		return []string{"coordinator", "local-worker"}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, "/v1/models", ""))
	var body struct {
		Object string             `json:"object"`
		Data   []modelCatalogItem `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Object != "list" || calls != 1 || runs != 0 {
		t.Fatal(w.Code, w.Body.String(), calls, runs)
	}
	want := []modelCatalogItem{{ID: "coordinator", Object: "model", Created: 0, OwnedBy: "nexusrouter"}, {ID: "local-worker", Object: "model", Created: 0, OwnedBy: "nexusrouter"}}
	if !reflect.DeepEqual(body.Data, want) || !strings.Contains(w.Body.String(), `"shutdown_date":null`) || strings.Contains(w.Body.String(), "endpoint") || strings.Contains(w.Body.String(), "credential") {
		t.Fatal(body, w.Body.String())
	}

	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"method", http.MethodPost, "/v1/models", http.StatusMethodNotAllowed},
		{"query", http.MethodGet, "/v1/models?secret=value", http.StatusBadRequest},
		{"origin", http.MethodGet, "/v1/models", http.StatusForbidden},
		{"unauthorized", http.MethodGet, "/v1/models", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			r := request(tc.method, tc.path, "")
			if tc.name == "origin" {
				r.Header.Set("Origin", "https://example.invalid")
			}
			if tc.name == "unauthorized" {
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || calls != before || !strings.Contains(w.Body.String(), `"type":`) || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String(), calls, before)
			}
		})
	}
}

func TestModelCatalogBackendFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		err  error
	}{
		{"missing", nil, nil},
		{"error", nil, errors.New("private backend detail")},
		{"invalid", []string{"bad id"}, nil},
		{"duplicate", []string{"same", "same"}, nil},
		{"excess", make([]string, 257), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.name != "missing" {
				s.Models = func(context.Context) ([]string, error) { return tc.ids, tc.err }
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(http.MethodGet, "/v1/models", ""))
			want := http.StatusServiceUnavailable
			if tc.name == "missing" {
				want = http.StatusNotImplemented
			}
			if w.Code != want || !strings.Contains(w.Body.String(), `"type":"server_error"`) || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestModelCatalogCapacityIsIndependent(t *testing.T) {
	s := services()
	started := make(chan struct{})
	release := make(chan struct{})
	s.Models = func(ctx context.Context) ([]string, error) {
		close(started)
		select {
		case <-release:
			return []string{"model"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	h, _ := New(token, 1, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), request(http.MethodGet, "/v1/models", ""))
	}()
	<-started
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, "/v1/models", ""))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	close(release)
	<-done
}

func TestModelCatalogRoundTripsThroughBuiltInOpenAIProvider(t *testing.T) {
	s := services()
	s.Models = func(context.Context) ([]string, error) {
		return []string{"coordinator", "local-worker"}, nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	provider, err := providers.NewHTTP(server.URL+"/v1", "openai_compatible", token, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := provider.Models(context.Background())
	if err != nil || !reflect.DeepEqual(ids, []string{"coordinator", "local-worker"}) {
		t.Fatal(ids, err)
	}
}
