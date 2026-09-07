package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

func TestLocalOnlyBlocksCloudAndUnapprovedTransportsAcrossRuntimeSurfaces(t *testing.T) {
	var httpCalls, factoryBuilds, codexLaunches atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		fmt.Fprintln(w, `{"data":[{"id":"cloud-model"}]}`)
	}))
	defer loopback.Close()

	cost := 0.0
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "local-only.db")
	cfg.Providers = []config.Provider{
		{ID: "remote-local", Kind: "openai_compatible", Endpoint: "https://provider.example"},
		{ID: "loopback-cloud", Kind: "openai_compatible", Endpoint: loopback.URL},
		{ID: "codex-cloud", Kind: "codex_app_server", Executable: "/fixture/codex"},
	}
	cfg.Models = []config.Model{
		{ID: "remote-local", Provider: "remote-local", Model: "local-model", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 4096, EstimatedCost: &cost, RAMBytes: 1},
		{ID: "loopback-cloud", Provider: "loopback-cloud", Model: "cloud-model", Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 4096, EstimatedCost: &cost},
		{ID: "codex-cloud", Provider: "codex-cloud", Model: "gpt-5.6-sol", Locality: "cloud", Capabilities: []string{"chat"}, ContextTokens: 4096, EstimatedCost: &cost},
	}
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		factoryBuilds.Add(1)
		return applicationProvider{}, nil
	})
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = healthProfile
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		codexLaunches.Add(1)
		return nil, errors.New("must not launch")
	}

	for _, model := range []string{"remote-local", "loopback-cloud", "codex-cloud"} {
		if _, err := svc.Run(context.Background(), Request{ModelID: model, Prompt: "local-only request"}); !errors.Is(err, ErrAdmission) {
			t.Fatalf("explicit %s was not denied: %v", model, err)
		}
	}
	if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "local-only request"}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("automatic routing crossed the egress boundary: %v", err)
	}
	report, err := svc.HealthReport(context.Background(), healthySupervisor())
	if err != nil || report.Validate() != nil {
		t.Fatalf("health report invalid: %+v %v", report, err)
	}
	if err := svc.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: "https://collector.example/v1/metrics"}); err == nil {
		t.Fatal("remote metrics transport admitted in local-only mode")
	}
	if factoryBuilds.Load() != 0 || codexLaunches.Load() != 0 || httpCalls.Load() != 0 {
		t.Fatalf("denied egress executed: factory=%d codex=%d http=%d", factoryBuilds.Load(), codexLaunches.Load(), httpCalls.Load())
	}
}
