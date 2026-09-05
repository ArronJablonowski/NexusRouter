package v1_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

type sdkProviderFactory func(context.Context, providers.Connection) (providers.Provider, error)

func (f sdkProviderFactory) Build(ctx context.Context, connection providers.Connection) (providers.Provider, error) {
	return f(ctx, connection)
}

type sdkProviderStream func(context.Context, providers.Request, func(providers.Chunk) error) error

func (s sdkProviderStream) Stream(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
	return s(ctx, req, emit)
}
func (sdkProviderStream) Models(context.Context) ([]string, error) { return []string{"fixture"}, nil }

func sdkProviderClient(t *testing.T, endpoint, mode, locality string, factory sdk.ProviderFactory) *sdk.Client {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = mode
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Workers.Max = 2
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: endpoint, APIKeyEnv: "SDK_PROVIDER_SECRET"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: locality, RAMBytes: 1, Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(sdk.ConfigOptions{
		ProjectFile: path, ProviderFactory: factory,
		ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil }),
		LookupSecret: func(name string) string {
			if name == "SDK_PROVIDER_SECRET" {
				return "provider-private-value"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSDKProviderFactoryExplicitAndLive(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = io.WriteString(w, "fixture") }))
	defer server.Close()
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "live"}[live], func(t *testing.T) {
			var builds atomic.Int32
			var observed atomic.Bool
			prefix := strings.Repeat("safe text ", 40)
			factory := sdkProviderFactory(func(ctx context.Context, c providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				if c.Version != 1 || c.ID != "local" || c.Kind != "ollama" || c.Endpoint != server.URL || c.APIKey != "provider-private-value" || c.Transport == nil {
					t.Errorf("incorrect connection metadata (credentials omitted)")
				}
				denied, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid", nil)
				if response, err := c.Transport.RoundTrip(denied); !errors.Is(err, policy.ErrEgress) || response != nil {
					t.Error("supplied transport allowed an unauthorized origin")
				}
				return sdkProviderStream(func(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
					if req.Model != "fixture" || len(req.Messages) == 0 {
						t.Error("request not forwarded")
					}
					r, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint, nil)
					response, err := c.Transport.RoundTrip(r)
					if err != nil {
						return err
					}
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if err != nil {
						return err
					}
					if err = emit(providers.Chunk{Text: prefix}); err != nil {
						return err
					}
					if live && !observed.Load() {
						t.Error("text callback was not delivered before provider completion")
					}
					return emit(providers.Chunk{Text: c.APIKey + " complete", Done: true, FinishReason: "stop"})
				}), nil
			})
			client := sdkProviderClient(t, server.URL, "local_only", "local", factory)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"}
			var result sdk.Result
			var err error
			var streamed strings.Builder
			if live {
				result, err = client.RunTextStream(ctx, req, func(text string) error { observed.Store(true); streamed.WriteString(text); return nil })
			} else {
				result, err = client.Run(ctx, req)
			}
			want := prefix + "[REDACTED] complete"
			if err != nil || result.Text != want || result.TaskID == "" || builds.Load() != 1 {
				t.Fatalf("execution failed: %v, builds=%d", err, builds.Load())
			}
			if live && streamed.String() != want {
				t.Error("live output differs from redacted result")
			}
		})
	}
	if requests.Load() != 2 {
		t.Fatalf("expected two requests through policy transport, got %d", requests.Load())
	}
}

func TestSDKProviderFactoryCannotBypassAdmission(t *testing.T) {
	for _, test := range []struct {
		name, mode, locality string
		capabilities         []string
	}{
		{"capability", "local_only", "local", []string{"unsupported"}},
		{"mode", "cloud_only", "local", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			factory := sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				calls.Add(1)
				return nil, errors.New("should not execute")
			})
			client := sdkProviderClient(t, "http://127.0.0.1:1", test.mode, test.locality, factory)
			result, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello", Capabilities: test.capabilities})
			if !errors.Is(err, sdk.ErrAdmission) || result.TaskID != "" || calls.Load() != 0 {
				t.Fatal("factory bypassed admission", result, err, calls.Load())
			}
		})
	}
}

func TestSDKProviderFactoryNilRetainsBuiltin(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected built-in request path %q", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"message":{"content":"built-in answer"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	defer server.Close()
	client := sdkProviderClient(t, server.URL, "local_only", "local", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
	if err != nil || result.Text != "built-in answer" || calls.Load() != 1 {
		t.Fatal("nil factory changed built-in behavior", result, err, calls.Load())
	}
}
