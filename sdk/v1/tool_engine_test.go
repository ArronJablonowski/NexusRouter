package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"go.yaml.in/yaml/v3"
)

func sdkToolOptions(t *testing.T) (sdk.ConfigOptions, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Tools.Enabled = false
	cfg.Runtime.MaxTurns = 3
	cfg.Tools.MaxTurns = 3
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "events.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 16384, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return sdk.ConfigOptions{ProjectFile: path, ResourceProfiler: sdkFixtureProfiler(func(context.Context) (resources.Measurement, error) { return sdkGoodMeasurement(), nil })}, cfg.Telemetry.Database
}

func sdkReadTool(calls *atomic.Int32) sdk.Tool {
	return sdk.Tool{Tool: providers.Tool{Name: "lookup_fact", Description: "Read a fixed public fact", Parameters: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","const":"answer"}},"required":["key"],"additionalProperties":false}`)}, Scope: "public_facts", ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (runtime.ToolResult, error) {
			calls.Add(1)
			return runtime.ToolResult{Content: "forty-two", Effect: runtime.NoEffect}, ctx.Err()
		}}
}

func TestSDKCustomToolRunsWithoutFilesystemTools(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var calls, turns atomic.Int32
	definition := sdkReadTool(&calls)
	originalSchema := string(definition.Tool.Parameters)
	parent := &sdk.ToolPolicy{Default: tools.Allow}
	permission := &sdk.ToolPolicy{Default: tools.Deny, Parent: parent, Rules: []tools.Rule{{Tool: "lookup_fact", Scope: "public_facts", Decision: tools.Allow}}}
	options.Tools = []sdk.Tool{definition}
	options.ToolPolicy = permission
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
			if len(req.Tools) != 1 || req.Tools[0].Name != "lookup_fact" || string(req.Tools[0].Parameters) != originalSchema {
				t.Errorf("unexpected or mutated tool catalog: %+v", req.Tools)
			}
			if turns.Add(1) == 1 {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call-1", Name: "lookup_fact", Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "call-1" || last.Content != "forty-two" {
				t.Errorf("missing custom tool result: %+v", last)
			}
			return emit(providers.Chunk{Text: "The answer is forty-two.", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	// Mutations after construction must not alter schemas or inherited policy.
	for i := range definition.Tool.Parameters {
		definition.Tool.Parameters[i] = ' '
	}
	permission.Rules[0].Decision = tools.Deny
	parent.Default = tools.Deny
	options.Tools[0].Handler = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Look up the answer"})
	if err != nil || out.Text != "The answer is forty-two." || out.Turns != 2 || calls.Load() != 1 {
		t.Fatalf("custom tool run: %+v, %v, calls=%d", out, err, calls.Load())
	}
}

func TestSDKCustomToolsDeniedByDefault(t *testing.T) {
	for _, decision := range []tools.Decision{"", tools.Deny, tools.Ask} {
		t.Run(string(decision), func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var calls atomic.Int32
			options.Tools = []sdk.Tool{sdkReadTool(&calls)}
			if decision != "" {
				options.ToolPolicy = &sdk.ToolPolicy{Default: decision}
			}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(ctx context.Context, req providers.Request, emit func(providers.Chunk) error) error {
					if len(req.Tools) != 0 {
						t.Errorf("denied tools advertised: %+v", req.Tools)
					}
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call-1", Name: "lookup_fact", Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
						return err
					}
					return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if err == nil || calls.Load() != 0 {
				t.Fatalf("denied handler ran or task succeeded: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestSDKInvalidToolRejectedBeforeStorage(t *testing.T) {
	options, database := sdkToolOptions(t)
	var calls atomic.Int32
	d := sdkReadTool(&calls)
	d.Tool.Parameters = json.RawMessage(`{"type":"not-a-type"}`)
	options.Tools = []sdk.Tool{d}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
	client, err := sdk.New(options)
	if client != nil || !errors.Is(err, sdk.ErrAdmission) {
		t.Fatalf("invalid tool accepted: %v", err)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor touched storage: %v", err)
	}
}

func TestSDKCustomToolAdmissionConstraints(t *testing.T) {
	for _, constraint := range []string{"unknown_context", "cloud_model"} {
		t.Run(constraint, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			body, err := os.ReadFile(options.ProjectFile)
			if err != nil {
				t.Fatal(err)
			}
			var cfg config.Settings
			if err = yaml.Unmarshal(body, &cfg); err != nil {
				t.Fatal(err)
			}
			if constraint == "unknown_context" {
				cfg.Models[0].ContextTokens = 0
			} else {
				cfg.Mode = "hybrid"
				cfg.Models[0].Locality = "cloud"
			}
			body, err = yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(options.ProjectFile, body, 0600); err != nil {
				t.Fatal(err)
			}
			var handlers, builds atomic.Int32
			options.Tools = []sdk.Tool{sdkReadTool(&handlers)}
			options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				return nil, errors.New("must not build")
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
			if !errors.Is(err, sdk.ErrAdmission) || out.TaskID != "" || builds.Load() != 0 || handlers.Load() != 0 {
				t.Fatalf("extension bypassed %s admission: %+v %v", constraint, out, err)
			}
		})
	}
}
