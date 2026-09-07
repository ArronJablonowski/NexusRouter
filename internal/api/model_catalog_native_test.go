package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func configuredCatalogFixture() routing.ModelCatalog {
	cost := 0.0
	return routing.ModelCatalog{Version: 1, ConfigID: strings.Repeat("a", 64), Models: []routing.ConfiguredModel{{Version: 1, ID: "local-fast", Provider: "local", Model: "fixture:latest", Locality: "local", Capabilities: []string{"chat", "code"}, ContextTokens: 8192, EstimatedCost: &cost, RAMBytes: 1024}}}
}

func configuredModelRequest(method string) *http.Request {
	r := request(method, "/v1/routing/models", "")
	r.Body = http.NoBody
	return r
}

func TestConfiguredModelCatalogHTTPIsDetailedButInert(t *testing.T) {
	catalog := configuredCatalogFixture()
	s := services()
	calls, runs := 0, 0
	s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) {
		calls++
		return catalog, nil
	}
	s.Run = func(context.Context, app.Request) (app.Result, error) {
		runs++
		return app.Result{}, errors.New("must not run")
	}
	s.Models = func(context.Context) ([]string, error) { return []string{"local-fast"}, nil }
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, configuredModelRequest(http.MethodGet))
	if w.Code != http.StatusOK || calls != 1 || runs != 0 || !strings.Contains(w.Body.String(), `"context_tokens":8192`) || !strings.Contains(w.Body.String(), `"estimated_cost":0`) {
		t.Fatal(w.Code, w.Body.String(), calls, runs)
	}
	for _, forbidden := range []string{"endpoint", "api_key", "credential", "executable", "healthy", "available"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("forbidden field leaked", forbidden, w.Body.String())
		}
	}
	// The OpenAI compatibility endpoint remains its intentionally minimal shape.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodGet, "/v1/models", ""))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "context_tokens") || strings.Contains(w.Body.String(), "fixture:latest") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestConfiguredModelCatalogHTTPFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service bool
		panic   bool
		catalog routing.ModelCatalog
		err     error
		status  int
	}{
		{name: "missing", status: http.StatusServiceUnavailable},
		{name: "error", service: true, err: errors.New("private backend"), status: http.StatusServiceUnavailable},
		{name: "panic", service: true, panic: true, status: http.StatusServiceUnavailable},
		{name: "invalid", service: true, catalog: routing.ModelCatalog{Version: 1, ConfigID: "private"}, status: http.StatusInternalServerError},
		{name: "nil models", service: true, catalog: routing.ModelCatalog{Version: 1, ConfigID: strings.Repeat("a", 64)}, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			if tc.service {
				s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) {
					if tc.panic {
						panic("private panic")
					}
					return tc.catalog, tc.err
				}
			}
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, configuredModelRequest(http.MethodGet))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		s := services()
		calls := 0
		s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) { calls++; return configuredCatalogFixture(), nil }
		h, _ := New(token, 1, s)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, configuredModelRequest(method))
		if w.Code != http.StatusMethodNotAllowed || calls != 0 {
			t.Fatal(method, w.Code, calls)
		}
	}
	for _, tc := range []struct {
		name   string
		status int
		build  func() *http.Request
	}{
		{name: "query", status: http.StatusBadRequest, build: func() *http.Request { return request(http.MethodGet, "/v1/routing/models?private=value", "") }},
		{name: "body", status: http.StatusBadRequest, build: func() *http.Request { return request(http.MethodGet, "/v1/routing/models", `{}`) }},
		{name: "origin", status: http.StatusForbidden, build: func() *http.Request {
			r := configuredModelRequest(http.MethodGet)
			r.Header.Set("Origin", "https://example.invalid")
			return r
		}},
		{name: "unauthorized", status: http.StatusUnauthorized, build: func() *http.Request {
			r := configuredModelRequest(http.MethodGet)
			r.Header.Del("Authorization")
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := services()
			calls := 0
			s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) { calls++; return configuredCatalogFixture(), nil }
			h, _ := New(token, 1, s)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, tc.build())
			if w.Code != tc.status || calls != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatal(tc.name, w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestConfiguredModelCatalogHTTPPreservesEmptyCatalog(t *testing.T) {
	s := services()
	s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) {
		return routing.ModelCatalog{Version: 1, ConfigID: strings.Repeat("a", 64), Models: []routing.ConfiguredModel{}}, nil
	}
	h, _ := New(token, 1, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, configuredModelRequest(http.MethodGet))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"models":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestConfiguredAndOpenAIModelCatalogsShareCapacity(t *testing.T) {
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
	calls := 0
	s.ConfiguredModels = func(context.Context) (routing.ModelCatalog, error) { calls++; return configuredCatalogFixture(), nil }
	h, _ := New(token, 1, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), request(http.MethodGet, "/v1/models", ""))
	}()
	<-started
	w := httptest.NewRecorder()
	h.ServeHTTP(w, configuredModelRequest(http.MethodGet))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" || calls != 0 {
		t.Fatal(w.Code, w.Header(), calls, w.Body.String())
	}
	close(release)
	<-done
}
