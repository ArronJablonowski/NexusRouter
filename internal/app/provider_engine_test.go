package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type applicationProviderFactory func(context.Context, providers.Connection) (providers.Provider, error)

func (f applicationProviderFactory) Build(ctx context.Context, c providers.Connection) (providers.Provider, error) {
	return f(ctx, c)
}

type applicationProvider struct{ discoveries *atomic.Int32 }

func (p applicationProvider) Models(context.Context) ([]string, error) {
	p.discoveries.Add(1)
	return []string{"a", "z"}, nil
}

func (p applicationProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "injected response", Done: true, FinishReason: "stop"})
}

func TestProviderEngineUsedForExplicitAutomaticAndChildExecution(t *testing.T) {
	for _, mode := range []string{"explicit", "automatic", "child"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Workers.DelegateModel = "z"
			var builds, discoveries atomic.Int32
			factory := applicationProviderFactory(func(_ context.Context, c providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				if c.Version != 1 || c.ID != "local" || c.Endpoint != cfg.Providers[0].Endpoint || c.Kind != "ollama" || c.APIKey != "" || c.Transport == nil {
					t.Error("incorrect provider connection")
				}
				return applicationProvider{&discoveries}, nil
			})
			svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, factory)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			var result Result
			switch mode {
			case "explicit":
				result, err = svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
			case "automatic":
				result, err = svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "hello"})
			case "child":
				result, err = svc.runDelegate(context.Background(), "hello", "", "parent-work", true, "", "")
			}
			if err != nil || result.Text != "injected response" || builds.Load() < 1 {
				t.Fatalf("provider engine bypassed: result=%+v err=%v builds=%d", result, err, builds.Load())
			}
			if mode == "automatic" && discoveries.Load() == 0 {
				t.Fatal("automatic discovery bypassed the provider engine")
			}
		})
	}
}

func TestProviderEngineNotBuiltBeforePrivacyAdmission(t *testing.T) {
	fixture, cfg := autoFixture(t)
	var builds atomic.Int32
	svc, err := NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	_, err = svc.Run(context.Background(), Request{ModelID: "not-configured", Prompt: "hello", LocalRequired: true})
	if err == nil || builds.Load() != 0 {
		t.Fatal("provider invoked before admission")
	}
}

func TestProviderEngineUsedForAuxiliaryModelsAndHealth(t *testing.T) {
	svc, _ := autoFixture(t)
	ctx := context.Background()
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "source task"})
	if err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	svc.providerFactory = applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, errors.New("factory unavailable")
	})
	if _, err := svc.AuditTask(ctx, source.TaskID, "z", 0); err == nil || builds.Load() != 1 {
		t.Fatal("audit bypassed configured factory", err, builds.Load())
	}
	if _, err := svc.SummarizeTask(ctx, source.TaskID, "a", 1, 0); err == nil || builds.Load() != 2 {
		t.Fatal("summary bypassed configured factory", err, builds.Load())
	}
	report, err := svc.HealthReport(ctx, healthySupervisor())
	if err != nil || builds.Load() != 3 {
		t.Fatal("health bypassed configured factory", err, builds.Load())
	}
	for _, check := range report.Checks {
		if check.Component == "provider" && check.Status == "healthy" {
			t.Fatal("unavailable factory reported healthy")
		}
	}
}
