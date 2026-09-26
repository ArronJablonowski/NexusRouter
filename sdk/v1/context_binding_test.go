package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type bindingEstimator struct{}

func (*bindingEstimator) Estimate(context.Context, providers.Request) (int, error) { return 1024, nil }

func TestConfiguredContextEstimatorBindingIsolation(t *testing.T) {
	cfg := config.Settings{Providers: []config.Provider{{ID: "p", Kind: "ollama", Endpoint: "http://127.0.0.1:11434", APIKeyEnv: "PRIVATE"}}, Models: []config.Model{{ID: "alias", Provider: "p", Model: "actual"}}}
	_, err := configuredContextEstimator(cfg, func(b []ContextModelBinding) (ContextEstimator, error) {
		if len(b) != 1 || b[0].ModelID != "alias" || b[0].Model != "actual" || b[0].ProviderID != "p" || b[0].Kind != "ollama" || b[0].Endpoint != "http://127.0.0.1:11434" {
			t.Fatalf("wrong effective binding: %+v", b)
		}
		b[0].Endpoint = "http://changed"
		return &bindingEstimator{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].Endpoint != "http://127.0.0.1:11434" {
		t.Fatal("factory mutated dispatch configuration")
	}
}
func TestConfiguredContextEstimatorRejectsFailures(t *testing.T) {
	for _, f := range []ContextEstimatorFactory{
		func([]ContextModelBinding) (ContextEstimator, error) { return nil, nil },
		func([]ContextModelBinding) (ContextEstimator, error) { return (*bindingEstimator)(nil), nil },
		func([]ContextModelBinding) (ContextEstimator, error) { return nil, errors.New("private failure") },
		func([]ContextModelBinding) (ContextEstimator, error) { panic("private panic") },
	} {
		if _, err := configuredContextEstimator(config.Settings{}, f); err != ErrAdmission {
			t.Fatalf("failure not sanitized: %v", err)
		}
	}
}

func TestSDKContextEstimatorFactoryConstruction(t *testing.T) {
	called := 0
	factory := func(bindings []ContextModelBinding) (ContextEstimator, error) {
		called++
		defaults := config.Defaults()
		if len(bindings) != len(defaults.Models) {
			t.Fatalf("bindings=%d models=%d", len(bindings), len(defaults.Models))
		}
		for i, m := range defaults.Models {
			if bindings[i].ModelID != m.ID || bindings[i].Model != m.Model || bindings[i].ProviderID != m.Provider {
				t.Fatal("loaded model mapping differs")
			}
			found := false
			for _, p := range defaults.Providers {
				if p.ID == m.Provider {
					found = true
					if bindings[i].Kind != p.Kind || bindings[i].Endpoint != p.Endpoint {
						t.Fatal("loaded provider mapping differs")
					}
				}
			}
			if !found {
				t.Fatal("provider missing")
			}
		}
		return &bindingEstimator{}, nil
	}
	if _, err := New(ConfigOptions{ContextEstimatorFactory: factory}); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("factory calls=%d", called)
	}
	if _, err := New(ConfigOptions{ContextEstimatorFactory: factory, ContextEstimator: &bindingEstimator{}}); err != ErrAdmission {
		t.Fatal("conflicting estimator accepted")
	}
	if called != 1 {
		t.Fatal("conflicting options invoked factory")
	}
	if _, err := New(ConfigOptions{ContextEstimatorFactory: factory, Overrides: map[string]string{"unknown_field": "private"}}); err != ErrAdmission {
		t.Fatal("invalid config accepted")
	}
	if called != 1 {
		t.Fatal("invalid config invoked factory")
	}
}
