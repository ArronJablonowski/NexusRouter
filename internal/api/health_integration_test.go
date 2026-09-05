package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darwinrouter/health"
	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/sessions"
)

func TestDetailedHealthUsesDiscoveryAndLiveSupervisor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var catalogs, inference atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			inference.Add(1)
			http.Error(w, "unexpected execution", 500)
			return
		}
		catalogs.Add(1)
		fmt.Fprintf(w, `{"data":[{"id":"fixture"},{"id":%q}]}`, token)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "cloud_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "health.db")
	cfg.Providers = []config.Provider{{ID: "provider", Kind: "openai_compatible", Endpoint: provider.URL + "/v1"}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "provider", Model: "fixture", Locality: "cloud", Capabilities: []string{"chat"}}}
	svc, err := app.NewService(cfg, func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := app.StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	for dispatcher.Health().Status != "healthy" {
		select {
		case <-ctx.Done():
			t.Fatal("supervisor not ready")
		case <-time.After(time.Millisecond):
		}
	}
	h, err := New(token, 1, Services{Run: func(context.Context, app.Request) (app.Result, error) { inference.Add(1); return app.Result{}, nil },
		Inspect:      func(context.Context, string) (sessions.Snapshot, error) { return sessions.Snapshot{}, nil },
		Health:       func(context.Context) error { return nil },
		HealthReport: func(ctx context.Context) (health.Report, error) { return svc.HealthReport(ctx, dispatcher.Health()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	read := func() (health.Report, int) {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/health", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || strings.Contains(string(body), token) || strings.Contains(string(body), provider.URL) || strings.Contains(string(body), cfg.Telemetry.Database) {
			t.Fatal("unsafe health response", err)
		}
		var report health.Report
		if json.Unmarshal(body, &report) != nil || report.Validate() != nil {
			t.Fatal(string(body))
		}
		return report, response.StatusCode
	}
	before, code := read()
	if code != 200 || !before.Ready || inference.Load() != 0 || catalogs.Load() != 1 {
		t.Fatal(code, before, inference.Load(), catalogs.Load())
	}
	if err := dispatcher.Close(); err != nil {
		t.Fatal(err)
	}
	after, code := read()
	if code != 503 || after.Ready || inference.Load() != 0 || catalogs.Load() != 2 {
		t.Fatal(code, after, inference.Load(), catalogs.Load())
	}
	found := false
	for _, check := range after.Checks {
		if check.Component == "supervisor" && check.Code == "supervisor_stopped" {
			found = true
		}
	}
	if !found {
		t.Fatal(after)
	}
}
