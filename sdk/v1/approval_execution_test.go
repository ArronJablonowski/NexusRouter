package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestSDKApprovalExecutionStatusAcrossRealToolLoop(t *testing.T) {
	options, _ := sdkToolOptions(t)
	var client *sdk.Client
	var reviewed approvals.Request
	var observed []approvals.ExecutionStatus
	check := func(ctx context.Context, r approvals.Request, state, call, writer, effect string) {
		s, err := client.ApprovalExecutionStatus(ctx, r.TaskID, r.ID)
		if err != nil || s.Validate() != nil || !s.Approval.Request.Matches(r) || s.Approval.State != state || s.CallState != call || s.ScopeWriterState != writer || s.RecordedEffect != effect {
			t.Fatalf("unexpected execution status %+v err=%v", s, err)
		}
		observed = append(observed, s)
	}
	options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "write_evidence", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "fixture", Handler: func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
		check(ctx, reviewed, approvals.Consumed, "open", "live", "")
		return runtime.ToolResult{Content: "saved", Effect: runtime.ConfirmedEffect}, nil
	}}}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Ask}
	options.ApprovalReviewer = func(ctx context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
		reviewed = p.Request
		check(ctx, reviewed, approvals.Pending, "open", "none", "")
		return "operator", true, nil
	}
	turns := 0
	options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
			turns++
			if turns == 1 {
				if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "write_evidence", Arguments: json.RawMessage(`{}`)}}); err != nil {
					return err
				}
				return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
			}
			return emit(providers.Chunk{Text: "finished", Done: true, FinishReason: "stop"})
		}), nil
	})
	var err error
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), sdk.Request{Version: 1, ModelID: "chat", Prompt: "write evidence"})
	if err != nil {
		t.Fatal(err)
	}
	check(context.Background(), reviewed, approvals.Consumed, "completed", "none", "confirmed")
	if turns != 2 || len(observed) != 3 || observed[2].TaskState != "completed" || observed[2].Approval.Request.TaskID != result.TaskID {
		t.Fatal(turns, observed, result)
	}
	for _, s := range observed {
		body, _ := json.Marshal(s)
		if strings.Contains(string(body), `"token"`) || strings.Contains(string(body), `"arguments":`) {
			t.Fatal("execution status exposed raw authority")
		}
	}
	if s, err := client.ApprovalExecutionStatus(context.Background(), "other", reviewed.ID); err == nil || s.Version != 0 {
		t.Fatal("cross-task approval exposed", s, err)
	}
}

func TestSDKApprovalExecutionStatusRejectsInvalidAndMissingStorage(t *testing.T) {
	options, path := sdkToolOptions(t)
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		s, err := client.ApprovalExecutionStatus(ctx, "task", "approval")
		if err == nil || s.Version != 0 {
			t.Fatal(s, err)
		}
	}
	for _, ids := range [][2]string{{"bad:task", "approval"}, {"task", "../approval"}, {"", "approval"}} {
		s, err := client.ApprovalExecutionStatus(context.Background(), ids[0], ids[1])
		if err == nil || s.Version != 0 {
			t.Fatal(s, err)
		}
	}
	for _, bad := range []*sdk.Client{nil, {}} {
		if _, err := bad.ApprovalExecutionStatus(context.Background(), "task", "approval"); !errors.Is(err, sdk.ErrAdmission) {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("status inspection created database", err)
	}
}
