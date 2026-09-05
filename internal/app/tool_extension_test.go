package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type extensionProvider struct {
	t         *testing.T
	wantTools []string
	streams   *atomic.Int32
	repeat    bool
}

func (p extensionProvider) Models(context.Context) ([]string, error) { return []string{"a", "z"}, nil }
func (p extensionProvider) Stream(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
	names := []string{}
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, p.wantTools) {
		p.t.Errorf("catalog %v, want %v", names, p.wantTools)
	}
	count := p.streams.Add(1)
	last := request.Messages[len(request.Messages)-1]
	if (last.Role != "tool" || p.repeat) && slices.Contains(names, "lookup") {
		if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: fmt.Sprintf("lookup-call-%d", count), Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
	}
	if last.Role == "tool" && last.Content != "trusted evidence" {
		p.t.Errorf("unexpected tool result %q", last.Content)
	}
	return emit(providers.Chunk{Text: "final answer", Done: true, FinishReason: "stop"})
}

func applicationExtension(t *testing.T, calls *atomic.Int32) *tools.Extension {
	t.Helper()
	extension, err := tools.NewExtension([]tools.Definition{{Tool: providers.Tool{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "fixture", ReadOnly: true, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		calls.Add(1)
		return runtime.ToolResult{Content: "trusted evidence", Effect: runtime.NoEffect}, nil
	}}}, &tools.Policy{Default: tools.Allow})
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func TestToolExtensionExplicitAutomaticAndChildIsolation(t *testing.T) {
	for _, mode := range []string{"explicit", "automatic", "child"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Workers.DelegateModel = "z"
			if mode == "automatic" {
				cfg.Mode = "hybrid"
				cfg.Providers = append(cfg.Providers, config.Provider{ID: "cloud", Kind: "openai_compatible", Endpoint: "https://example.invalid"})
				cfg.Models[1].Provider, cfg.Models[1].Locality = "cloud", "cloud"
				cfg.Workers.DelegateModel = "a"
			}
			var calls, streams atomic.Int32
			catalog := []string{"delegate", "delegate_batch", "lookup"}
			if mode == "child" {
				catalog = []string{}
			}
			factory := applicationProviderFactory(func(_ context.Context, connection providers.Connection) (providers.Provider, error) {
				if connection.ID == "cloud" {
					t.Error("extension allowed cloud discovery or inference")
				}
				return extensionProvider{t: t, wantTools: catalog, streams: &streams}, nil
			})
			svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, applicationExtension(t, &calls))
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			var result Result
			if mode == "child" {
				result, err = svc.runDelegate(context.Background(), "inspect", "", "parent-work", true, "", "")
			} else {
				model := "a"
				if mode == "automatic" {
					model = "auto"
				}
				result, err = svc.Run(context.Background(), Request{ModelID: model, Prompt: "inspect"})
			}
			wantCalls, wantStreams := int32(1), int32(2)
			if mode == "child" {
				wantCalls, wantStreams = 0, 1
			}
			if err != nil || result.Text != "final answer" || calls.Load() != wantCalls || streams.Load() != wantStreams {
				t.Fatalf("result=%+v err=%v calls=%d streams=%d", result, err, calls.Load(), streams.Load())
			}
			db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			state, err := sessions.Replay(context.Background(), db, result.TaskID)
			if err != nil || state.State != "completed" || state.Privacy != "local_only" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
		})
	}
}

func TestToolExtensionRejectsCloudAndUnknownContextBeforeProvider(t *testing.T) {
	for _, kind := range []string{"cloud", "unknown-context"} {
		t.Run(kind, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Mode = "hybrid"
			if kind == "cloud" {
				cfg.Models[0].Locality = "cloud"
			} else {
				cfg.Models[0].ContextTokens = 0
			}
			var calls, builds atomic.Int32
			factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				return nil, nil
			})
			svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, applicationExtension(t, &calls))
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "inspect"}); err == nil || builds.Load() != 0 || calls.Load() != 0 {
				t.Fatal("extension admission bypassed", err)
			}
			if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
				t.Fatal("denied task touched storage", err)
			}
		})
	}
}

func TestToolExtensionTurnsAndContextBounded(t *testing.T) {
	for _, kind := range []string{"turns", "context"} {
		t.Run(kind, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Tools.MaxTurns = 2
			if kind == "context" {
				for i := range cfg.Models {
					cfg.Models[i].ContextTokens = 1
				}
			}
			var calls, streams atomic.Int32
			factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return extensionProvider{t: t, wantTools: []string{"lookup"}, streams: &streams, repeat: true}, nil
			})
			svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, applicationExtension(t, &calls))
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if _, err := svc.Run(context.Background(), Request{ModelID: "auto", Prompt: "inspect"}); err == nil {
				t.Fatal("limits ignored")
			}
			if (kind == "turns" && streams.Load() != 2) || (kind == "context" && streams.Load() != 0) {
				t.Fatal("provider exceeded bounds", streams.Load())
			}
		})
	}
}

func TestToolExtensionRejectsDurableAuthorityBeforeDispatch(t *testing.T) {
	fixture, cfg := autoFixture(t)
	var calls, builds atomic.Int32
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, nil
	})
	svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, applicationExtension(t, &calls))
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	ctx := context.Background()
	request := Request{ModelID: "a", Prompt: "inspect"}
	if _, err := svc.Submit(ctx, "extension-intake-key", request); err != ErrAdmission {
		t.Fatal("extension submission admitted", err)
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("denied submission opened storage", err)
	}
	// Work admitted without an extension may not acquire newly installed tool
	// authority when a different service claims it under the same config digest.
	status, err := fixture.Submit(ctx, "original-intake-key", request)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claim, err := db.ClaimSubmission(ctx, fixture.submissionConfigDigest(), time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Submission(ctx, status.ID)
	if err != nil {
		t.Fatal(err)
	}
	request.submissionID, request.submissionToken = status.ID, claim.Token
	if result, err := svc.Run(ctx, request); err != ErrAdmission || result.TaskID != "" {
		t.Fatal("queued task acquired extension authority", result, err)
	}
	after, err := db.Submission(ctx, status.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected run changed ledger", before, after, err)
	}
	if builds.Load() != 0 || calls.Load() != 0 {
		t.Fatal("rejected durable extension invoked code")
	}
}
