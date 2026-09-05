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
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"go.yaml.in/yaml/v3"
)

func TestSDKCreateFileBuiltinRequiresApproval(t *testing.T) {
	for _, approve := range []bool{false, true} {
		name := "without_authority"
		if approve {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			options, database := sdkToolOptions(t)
			cfg, err := config.Load(config.Options{ProjectFile: options.ProjectFile})
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			cfg.Tools.Enabled, cfg.Tools.CreateEnabled = true, true
			cfg.Tools.ReadRoot, cfg.Tools.CreateRoot = root, root
			body, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(options.ProjectFile, body, 0600); err != nil {
				t.Fatal(err)
			}
			const content = "approved SDK content 世界\n"
			target := filepath.Join(root, "created.txt")
			arguments, err := json.Marshal(map[string]string{"path": "created.txt", "content": content})
			if err != nil {
				t.Fatal(err)
			}
			var builds, turns, reviews atomic.Int32
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				return sdkProviderStream(func(_ context.Context, request providers.Request, emit func(providers.Chunk) error) error {
					found := false
					for _, tool := range request.Tools {
						found = found || tool.Name == "create_file"
					}
					if !found {
						t.Error("builtin absent from provider catalog")
					}
					if turns.Add(1) == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "create-call", Name: "create_file", Arguments: arguments}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || last.ToolCallID != "create-call" || last.Content != `{"created":true}` {
						t.Error("missing confirmed builtin result")
					}
					return emit(providers.Chunk{Text: "File created.", Done: true, FinishReason: "stop"})
				}), nil
			})
			if approve {
				options.ApprovalReviewer = func(_ context.Context, prompt sdk.ApprovalPrompt) (string, bool, error) {
					reviews.Add(1)
					if prompt.Request.ToolName != "create_file" || string(prompt.Arguments) != string(arguments) {
						t.Error("approval did not bind exact builtin arguments")
					}
					if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
						t.Error("file existed before approval")
					}
					// Review receives an isolated preview, not executable arguments.
					clear(prompt.Arguments)
					return "sdk-test-operator", true, nil
				}
			}
			client, err := sdk.New(options)
			if !approve {
				if !errors.Is(err, sdk.ErrAdmission) || builds.Load() != 0 || turns.Load() != 0 {
					t.Fatal("missing authority did not reject before provider construction")
				}
				if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected construction touched database")
				}
				if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
					t.Fatal("rejected construction touched create root")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			result, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Create the file"})
			if err != nil || result.Text != "File created." || reviews.Load() != 1 || turns.Load() != 2 || builds.Load() != 1 {
				t.Fatal("approved SDK builtin execution failed", err)
			}
			created, err := os.ReadFile(target)
			if err != nil || string(created) != content {
				t.Fatal("approved content changed or missing")
			}
		})
	}
}
