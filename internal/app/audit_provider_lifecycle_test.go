package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

type auditLifecycleProvider struct {
	stream     func(context.Context, providers.Request, func(providers.Chunk) error) error
	closed     int
	closePanic bool
}

func (p *auditLifecycleProvider) Models(context.Context) ([]string, error) { return nil, nil }
func (p *auditLifecycleProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p.stream(ctx, r, emit)
}
func (p *auditLifecycleProvider) Close() error {
	p.closed++
	if p.closePanic {
		panic("fixture close")
	}
	return nil
}

func auditLifecycleAdapter(launch codexLaunch) *codexAuxiliaryProvider {
	return &codexAuxiliaryProvider{settings: config.Settings{Mode: "hybrid"}, provider: config.Provider{Kind: "codex_app_server", Executable: "/fixture/codex"}, model: config.Model{Model: "gpt-5.6-sol", Locality: "cloud"}, privacy: "cloud_allowed", launch: launch}
}
func auditLifecycleRequest() providers.Request {
	return providers.Request{Model: "gpt-5.6-sol", Messages: []providers.Message{{Role: "system", Content: "audit rules"}, {Role: "user", Content: "untrusted evidence"}}}
}

func TestCodexAuditProviderRejectsShapeWithoutLaunchAndConsumesUse(t *testing.T) {
	for name, mutate := range map[string]func(*providers.Request){
		"model":  func(r *providers.Request) { r.Model = "other" },
		"roles":  func(r *providers.Request) { r.Messages[0].Role = "user" },
		"extra":  func(r *providers.Request) { r.Messages = append(r.Messages, r.Messages[1]) },
		"tools":  func(r *providers.Request) { r.Tools = []providers.Tool{{Name: "delegate"}} },
		"schema": func(r *providers.Request) { r.JSONSchema = json.RawMessage(`{`) },
		"call":   func(r *providers.Request) { r.Messages[1].ToolCallID = "call" },
	} {
		t.Run(name, func(t *testing.T) {
			launches := 0
			p := auditLifecycleAdapter(func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				return nil, ErrAdmission
			})
			if _, err := p.Models(context.Background()); err == nil || launches != 0 {
				t.Fatal("Models launched or admitted")
			}
			r := auditLifecycleRequest()
			mutate(&r)
			if p.Stream(context.Background(), r, func(providers.Chunk) error { return nil }) == nil {
				t.Fatal("shape admitted")
			}
			if p.Stream(context.Background(), auditLifecycleRequest(), func(providers.Chunk) error { return nil }) == nil || launches != 0 {
				t.Fatal("failed attempt reused")
			}
		})
	}
}

func TestCodexAuditProviderLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "stream_error", "stream_panic", "callback_error", "cancel", "close_panic", "launch_error", "launch_panic", "typed_nil", "provider_and_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture := &auditLifecycleProvider{closePanic: mode == "close_panic"}
			fixture.stream = func(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				if mode == "stream_panic" {
					panic("fixture stream")
				}
				if mode == "stream_error" {
					return errors.New("fixture stream")
				}
				if mode == "cancel" {
					cancel()
					return ctx.Err()
				}
				return emit(providers.Chunk{Text: "audit", Done: true, FinishReason: "stop"})
			}
			var cwd string
			launches := 0
			p := auditLifecycleAdapter(func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				cwd = spec.CWD
				info, err := os.Stat(cwd)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("private directory missing or permissive")
				}
				switch mode {
				case "launch_panic":
					panic("fixture launch")
				case "launch_error":
					return nil, ErrAdmission
				case "typed_nil":
					var nilProvider *auditLifecycleProvider
					return nilProvider, nil
				case "provider_and_error":
					return fixture, ErrAdmission
				}
				return fixture, nil
			})
			if launches != 0 {
				t.Fatal("eager launch")
			}
			err := p.Stream(ctx, auditLifecycleRequest(), func(providers.Chunk) error {
				if mode == "callback_error" {
					return errors.New("fixture callback")
				}
				return nil
			})
			if (mode == "success" || mode == "close_panic") != (err == nil) {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if launches != 1 || cwd == "" {
				t.Fatal("launch count")
			}
			if _, err := os.Stat(cwd); !os.IsNotExist(err) {
				t.Fatal("private directory leaked")
			}
			wantClosed := 1
			if mode == "launch_error" || mode == "launch_panic" || mode == "typed_nil" {
				wantClosed = 0
			}
			if fixture.closed != wantClosed {
				t.Fatalf("close count %d want %d", fixture.closed, wantClosed)
			}
			if p.Stream(context.Background(), auditLifecycleRequest(), func(providers.Chunk) error { return nil }) == nil || launches != 1 {
				t.Fatal("adapter reused")
			}
		})
	}
}

func TestCodexOwnedProviderCleanupIdempotent(t *testing.T) {
	fixture := &auditLifecycleProvider{}
	p := auditLifecycleAdapter(nil)
	p.model.ReasoningEffort = "medium"
	var cwd string
	adapter, closeProvider, err := openOwnedCodexProvider(context.Background(), p.settings, p.provider, p.model, p.privacy, func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
		cwd = spec.CWD
		if spec.ReasoningEffort != "medium" {
			t.Fatal("configured reasoning effort was not forwarded to the Codex launch")
		}
		return fixture, nil
	})
	owned, ok := adapter.(*ownedCodexProvider)
	if err != nil || !ok || owned.taskProvider != fixture {
		t.Fatal("open failed")
	}
	closeProvider()
	closeProvider()
	if fixture.closed != 1 {
		t.Fatal("repeated close")
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatal("directory retained")
	}
}

func TestCodexAuditProviderInvalidContextOrCallbackNeverLaunches(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		emit func(providers.Chunk) error
	}{
		{"nil context", nil, func(providers.Chunk) error { return nil }},
		{"canceled", canceled, func(providers.Chunk) error { return nil }},
		{"nil callback", context.Background(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launches := 0
			p := auditLifecycleAdapter(func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				return nil, ErrAdmission
			})
			if err := p.Stream(tc.ctx, auditLifecycleRequest(), tc.emit); err == nil || launches != 0 {
				t.Fatal("invalid call launched")
			}
			if err := p.Stream(context.Background(), auditLifecycleRequest(), func(providers.Chunk) error { return nil }); err == nil || launches != 0 {
				t.Fatal("invalid first attempt allowed reuse")
			}
		})
	}
}
