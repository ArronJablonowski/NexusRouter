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

	"github.com/ArronJablonowski/NexusRouter/providers"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKReplaceFileBuiltinApproval(t *testing.T) {
	for _, mode := range []string{"no-reviewer", "denied", "approved"} {
		t.Run(mode, func(t *testing.T) {
			options, database := sdkToolOptions(t)
			root := t.TempDir()
			target := filepath.Join(root, "existing.txt")
			const original = "original 世界\n"
			const replacement = "reviewed replacement 世界\n"
			if err := os.WriteFile(target, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			options.Overrides = map[string]string{"tools.enabled": "true", "tools.read_root": root, "tools.replace_enabled": "true", "tools.replace_root": root}
			arguments, _ := json.Marshal(map[string]string{"path": "existing.txt", "expected_content": original, "content": replacement})
			var builds, turns, reviews atomic.Int32
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				builds.Add(1)
				return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					found := false
					for _, tool := range r.Tools {
						found = found || tool.Name == "replace_file"
					}
					if !found {
						t.Error("replace absent from catalog")
					}
					if turns.Add(1) == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "replace-call", Name: "replace_file", Arguments: arguments}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					last := r.Messages[len(r.Messages)-1]
					if last.Role != "tool" || last.ToolCallID != "replace-call" {
						t.Error("missing paired tool result")
					}
					return emit(providers.Chunk{Text: "Finished review.", Done: true, FinishReason: "stop"})
				}), nil
			})
			if mode != "no-reviewer" {
				options.ApprovalReviewer = func(_ context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
					reviews.Add(1)
					if p.Request.ToolName != "replace_file" || string(p.Arguments) != string(arguments) {
						t.Error("approval mismatch")
					}
					before, err := os.ReadFile(target)
					if err != nil || string(before) != original {
						t.Error("file changed before approval")
					}
					clear(p.Arguments)
					return "sdk-operator", mode == "approved", nil
				}
			}
			client, err := sdk.New(options)
			if mode == "no-reviewer" {
				if !errors.Is(err, sdk.ErrAdmission) || builds.Load() != 0 {
					t.Fatal("unreviewed mutation configured", err)
				}
				if _, err := os.Stat(database); !os.IsNotExist(err) {
					t.Fatal("rejection created DB", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.Submit(context.Background(), "replace-submission-key", sdk.Request{Version: 1, ModelID: "chat", Prompt: "Replace existing content"}); !errors.Is(err, sdk.ErrAdmission) {
					t.Fatal("replace durable submission admitted", err)
				}
				if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected submission created storage", err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				_, runErr := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Review and replace the existing file"})
				if mode == "approved" && runErr != nil {
					t.Fatal(runErr)
				}
				if reviews.Load() != 1 {
					t.Fatal("wrong approval count", reviews.Load())
				}
			}
			content, err := os.ReadFile(target)
			want := original
			if mode == "approved" {
				want = replacement
			}
			if err != nil || string(content) != want {
				t.Fatal("wrong file outcome", err)
			}
		})
	}
}
