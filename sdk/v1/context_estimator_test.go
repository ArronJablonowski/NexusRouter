package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"go.yaml.in/yaml/v3"
)

type sdkContextEstimator func(context.Context, providers.Request) (int, error)

func (f sdkContextEstimator) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return f(ctx, r)
}

func TestSDKContextEstimatorToolLoopIsolationAndPerTurnLimit(t *testing.T) {
	for _, mode := range []string{"nil", "mutating", "high_first", "high_second"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var handlers, turns, estimates atomic.Int32
			definition := sdkReadTool(&handlers)
			originalSchema := string(definition.Tool.Parameters)
			options.Tools = []sdk.Tool{definition}
			options.ToolPolicy = &tools.Policy{Default: tools.Allow}
			if mode != "nil" {
				options.ContextEstimator = sdkContextEstimator(func(ctx context.Context, r providers.Request) (int, error) {
					n := estimates.Add(1)
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 3*time.Second || r.Model != "fixture" {
						t.Errorf("unbounded or non-model-specific estimate: %q", r.Model)
					}
					if mode == "high_first" || (mode == "high_second" && n == 2) {
						return 16385, nil
					}
					// Nested mutations must not change either current provider input
					// or historical tool arguments on the next turn.
					r.Messages[0].Content = "private-mutated-prompt"
					r.Tools[0].Name = "private_mutated_tool"
					r.Tools[0].Parameters[0] = '['
					for _, message := range r.Messages {
						for _, call := range message.ToolCalls {
							call.Arguments[0] = '['
						}
					}
					return 0, nil // Cannot lower the built-in conservative estimate.
				})
			}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					n := turns.Add(1)
					if r.Model != "fixture" || len(r.Tools) != 1 || r.Tools[0].Name != "lookup_fact" || string(r.Tools[0].Parameters) != originalSchema {
						t.Errorf("estimator mutated provider catalog: %+v", r.Tools)
					}
					for _, m := range r.Messages {
						if strings.Contains(m.Content, "private-mutated") {
							t.Error("estimator mutated prompt")
						}
						for _, call := range m.ToolCalls {
							if string(call.Arguments) != `{"key":"answer"}` {
								t.Error("estimator mutated call arguments")
							}
						}
					}
					if n == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "lookup-call", Name: "lookup_fact", Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					last := r.Messages[len(r.Messages)-1]
					if last.Role != "tool" || last.Content != "forty-two" {
						t.Errorf("lost tool result: %+v", last)
					}
					return emit(providers.Chunk{Text: "forty-two", Done: true, FinishReason: "stop"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Look up the answer"})
			switch mode {
			case "high_first":
				if err == nil || turns.Load() != 0 || handlers.Load() != 0 || estimates.Load() != 1 {
					t.Fatal("oversized first turn dispatched", out, err, turns.Load(), handlers.Load(), estimates.Load())
				}
			case "high_second":
				if err == nil || turns.Load() != 1 || handlers.Load() != 1 || estimates.Load() != 2 {
					t.Fatal("oversized next turn dispatched", out, err, turns.Load(), handlers.Load(), estimates.Load())
				}
			default:
				if err != nil || out.Text != "forty-two" || out.Turns != 2 || turns.Load() != 2 || handlers.Load() != 1 {
					t.Fatal("tool loop failed", out, err, turns.Load(), handlers.Load())
				}
				if mode == "mutating" && estimates.Load() != 2 {
					t.Fatal("not estimated each turn", estimates.Load())
				}
			}
		})
	}
}

func TestSDKContextEstimatorAutomaticAdmissionAndPrivateFailures(t *testing.T) {
	for _, mode := range []string{"auto_high", "auto_error", "auto_panic", "explicit_error", "explicit_panic"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var turns, estimates atomic.Int32
			auto := strings.HasPrefix(mode, "auto")
			options.ContextEstimator = sdkContextEstimator(func(ctx context.Context, r providers.Request) (int, error) {
				estimates.Add(1)
				if r.Model != "fixture" {
					t.Error("estimate not model-specific", r.Model)
				}
				if strings.HasSuffix(mode, "panic") {
					panic("private-estimator-failure")
				}
				if strings.HasSuffix(mode, "error") {
					return 0, errors.New("private-estimator-failure")
				}
				return 16385, nil
			})
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					turns.Add(1)
					return emit(providers.Chunk{Text: "should not dispatch", Done: true, FinishReason: "stop"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			model := "chat"
			if auto {
				model = "auto"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = client.Run(ctx, sdk.Request{Version: 1, ModelID: model, Prompt: "hello"})
			if err == nil || strings.Contains(err.Error(), "private-estimator-failure") || turns.Load() != 0 || estimates.Load() != 1 {
				t.Fatal("estimate failure bypassed admission or leaked", err, turns.Load(), estimates.Load())
			}
		})
	}
}

type sdkEstimatorModels struct{ sdkProviderStream }

func (sdkEstimatorModels) Models(context.Context) ([]string, error) {
	return []string{"fixture", "alternate"}, nil
}

func updateEstimatorConfig(t *testing.T, options sdk.ConfigOptions, update func(*config.Settings)) {
	t.Helper()
	body, err := os.ReadFile(options.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Settings
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	update(&cfg)
	body, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.ProjectFile, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSDKContextEstimatorSelectsViableCandidate(t *testing.T) {
	for _, mode := range []string{"high", "error"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			updateEstimatorConfig(t, options, func(cfg *config.Settings) {
				alternate := cfg.Models[0]
				alternate.ID, alternate.Model = "alternate", "alternate"
				cfg.Models = append(cfg.Models, alternate)
			})
			var mu sync.Mutex
			estimated := map[string]int{}
			options.ContextEstimator = sdkContextEstimator(func(ctx context.Context, r providers.Request) (int, error) {
				mu.Lock()
				estimated[r.Model]++
				mu.Unlock()
				if r.Model == "fixture" {
					if mode == "error" {
						return 0, errors.New("private-unmeasurable-candidate")
					}
					return 16385, nil
				}
				if r.Model != "alternate" {
					t.Error("unexpected estimator model", r.Model)
				}
				return 0, nil
			})
			var turns atomic.Int32
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkEstimatorModels{sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					turns.Add(1)
					if r.Model != "alternate" {
						t.Error("unfit candidate dispatched", r.Model)
					}
					return emit(providers.Chunk{Text: "viable alternative", Done: true, FinishReason: "stop"})
				})}, nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "auto", Prompt: "hello"})
			if err != nil || out.Text != "viable alternative" || turns.Load() != 1 {
				t.Fatal("viable route denied", out, err, turns.Load())
			}
			mu.Lock()
			defer mu.Unlock()
			if estimated["fixture"] != 1 || estimated["alternate"] != 2 || estimated[""] != 0 {
				t.Fatal("missing candidate/runtime estimates", estimated)
			}
		})
	}
}

func TestSDKContextEstimatorRejectsUnknownExplicitWindow(t *testing.T) {
	options, _ := sdkToolOptions(t)
	updateEstimatorConfig(t, options, func(cfg *config.Settings) { cfg.Models[0].ContextTokens = 0 })
	options.ContextEstimator = sdkContextEstimator(func(context.Context, providers.Request) (int, error) { return 0, nil })
	var turns atomic.Int32
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			turns.Add(1)
			return emit(providers.Chunk{Text: "unsafe", Done: true, FinishReason: "stop"})
		}), nil
	})
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "hello"})
	if err == nil || turns.Load() != 0 {
		t.Fatal("unknown window admitted", err, turns.Load())
	}
}

// A trusted counter must reach both SDK admission and subsequent tool dispatch.
// Large tool history exercises the byte-floor override without dropping history.
func TestSDKBoundCounterToolHistory(t *testing.T) {
	for _, mode := range []string{"exact", "unsupported", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var handlers, turns, estimates atomic.Int32
			options.Tools = []sdk.Tool{sdkReadTool(&handlers)}
			options.ToolPolicy = &tools.Policy{Default: tools.Allow}
			counter, err := providers.NewBoundTokenCounter("fixture", strings.Repeat("a", 64), strings.Repeat("b", 64), func(ctx context.Context, r providers.Request) (int, bool, error) {
				estimates.Add(1)
				if len(r.Tools) != 1 || r.Tools[0].Name != "lookup_fact" {
					t.Error("missing catalog")
				}
				if turns.Load() > 0 {
					last := r.Messages[len(r.Messages)-1]
					if last.Role != "tool" || last.Content != "forty-two" {
						t.Error("missing live tool history")
					}
					if mode == "overflow" {
						return 16384, true, nil
					}
				}
				return 100, mode != "unsupported", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			options.ContextEstimator = counter
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					if turns.Add(1) == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "lookup", Name: "lookup_fact", Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: strings.Repeat("word ", 4000)})
			switch mode {
			case "exact":
				if err != nil || out.Text != "done" || turns.Load() != 2 || handlers.Load() != 1 {
					t.Fatal(out, err, turns.Load(), handlers.Load())
				}
			case "unsupported":
				if err == nil || turns.Load() != 0 {
					t.Fatal("unsupported request bypassed byte floor", err, turns.Load())
				}
			case "overflow":
				if err == nil || turns.Load() != 1 || handlers.Load() != 1 {
					t.Fatal("oversized next turn dispatched", err, turns.Load())
				}
			}
			if estimates.Load() == 0 {
				t.Fatal("counter never invoked")
			}
		})
	}
}
