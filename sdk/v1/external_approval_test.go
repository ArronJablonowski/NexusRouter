package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestSDKExternalApprovalControlsWaitingExecution(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[allowed], func(t *testing.T) {
			options, _ := sdkToolOptions(t)
			var calls, turns atomic.Int32
			tool := sdkReadTool(&calls)
			tool.ReadOnly = false
			tool.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				calls.Add(1)
				return runtime.ToolResult{Content: "saved", Effect: runtime.ConfirmedEffect}, nil
			}
			options.Tools = []sdk.Tool{tool}
			options.ToolPolicy = &tools.Policy{Default: tools.Ask}
			previews := make(chan sdk.ApprovalPrompt, 1)
			options.ApprovalPresenter = func(ctx context.Context, p sdk.ApprovalPrompt) error {
				select {
				case previews <- p:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					if turns.Add(1) == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: tool.Tool.Name, Arguments: json.RawMessage(`{"key":"answer"}`)}}); err != nil {
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
			controlOptions := options
			controlOptions.Tools = nil
			controlOptions.ToolPolicy = nil
			controlOptions.ApprovalPresenter = nil
			controller, err := sdk.New(controlOptions)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "save"})
				done <- err
			}()
			var p sdk.ApprovalPrompt
			select {
			case p = <-previews:
			case <-ctx.Done():
				t.Fatal("no preview")
			}
			if calls.Load() != 0 || string(p.Arguments) != `{"key":"answer"}` {
				t.Fatal("executed before decision", calls.Load())
			}
			command := approvals.Command{Expected: p.Request, ID: "operator_decision", Allowed: allowed}
			stale := command
			stale.Expected.PolicyDigest = strings.Repeat("0", 64)
			if _, err = controller.DecideApproval(ctx, stale, "operator"); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("stale proposal accepted", err)
			}
			if _, err = controller.DecideApproval(ctx, command, "operator"); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("waiting task stuck")
			}
			if allowed && (err != nil || calls.Load() != 1) {
				t.Fatal(err, calls.Load())
			}
			if !allowed && (err == nil || calls.Load() != 0) {
				t.Fatal(err, calls.Load())
			}
			replayed, err := controller.DecideApproval(ctx, command, "operator")
			if err != nil {
				t.Fatal(err)
			}
			want := approvals.Denied
			if allowed {
				want = approvals.Consumed
			}
			if replayed.State != want {
				t.Fatal(replayed.State)
			}
			if _, err = controller.DecideApproval(ctx, command, "different_operator"); !errors.Is(err, approvals.ErrConflict) {
				t.Fatal("actor changed on retry", err)
			}
		})
	}
}
