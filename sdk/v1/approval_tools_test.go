package v1_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestSDKApprovalBackedWrite(t *testing.T) {
	for _, mode := range []string{"ask", "allow", "deny", "cancel", "secret_actor", "secret_scope"} {
		t.Run(mode, func(t *testing.T) {
			options, database := sdkToolOptions(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const secret = "private-approval-fixture"
			options.LookupSecret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					return secret
				}
				return ""
			}
			args := json.RawMessage(`{"text":"private-approval-fixture"}`)
			artifact := filepath.Join(t.TempDir(), "approved.txt")
			var reviewed approvals.Request
			reviews, calls, turns := 0, 0, 0
			scope := "artifact"
			if mode == "secret_scope" {
				scope = secret
			}
			options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "save_artifact", Description: "Save operator-approved text", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)}, Scope: scope, Handler: func(ctx context.Context, raw json.RawMessage) (runtime.ToolResult, error) {
				calls++
				if string(raw) != string(args) {
					t.Fatal("review mutated execution arguments")
				}
				db, err := telemetry.OpenReadOnly(ctx, database)
				if err != nil {
					t.Fatal(err)
				}
				r, err := db.ReadApproval(ctx, reviewed.ID)
				db.Close()
				if err != nil || r.State != approvals.Consumed {
					t.Fatal("write before consumed approval", r, err)
				}
				if err = os.WriteFile(artifact, raw, 0600); err != nil {
					return runtime.ToolResult{Effect: runtime.UncertainEffect}, err
				}
				return runtime.ToolResult{Content: "saved", Effect: runtime.ConfirmedEffect}, nil
			}}}
			decision := tools.Ask
			if mode == "allow" {
				decision = tools.Allow
			}
			options.ToolPolicy = &sdk.ToolPolicy{Default: decision}
			options.ApprovalReviewer = func(_ context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
				reviews++
				reviewed = p.Request
				sum := sha256.Sum256(args)
				if string(p.Arguments) != string(args) || p.Description != "Save operator-approved text" || p.Request.ArgumentsDigest != hex.EncodeToString(sum[:]) {
					t.Fatal("incorrect review preview", p)
				}
				for i := range p.Arguments {
					p.Arguments[i] = ' '
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "secret_actor" {
					return secret, true, nil
				}
				return "authenticated-test-operator", mode != "deny", nil
			}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
					turns++
					if len(r.Tools) != 1 || r.Tools[0].Name != "save_artifact" {
						t.Fatal("missing write tool", r.Tools)
					}
					if turns == 1 {
						if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "save_artifact", Arguments: args}}); err != nil {
							return err
						}
						return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
					}
					return emit(providers.Chunk{Text: "Saved approved artifact.", Done: true, FinishReason: "stop"})
				}), nil
			})
			client, err := sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "Save text"})
			success := mode == "ask" || mode == "allow"
			if success {
				if err != nil || calls != 1 || reviews != 1 || out.Text != "Saved approved artifact." {
					t.Fatal(out, err, calls, reviews)
				}
				body, err := os.ReadFile(artifact)
				if err != nil || string(body) != string(args) {
					t.Fatal(string(body), err)
				}
			} else {
				if err == nil || calls != 0 {
					t.Fatal("unapproved write", out, err, calls)
				}
				if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("artifact unexpectedly exists", err)
				}
			}
			if mode == "secret_scope" && reviews != 0 {
				t.Fatal("secret binding reached review")
			}
			if reviewed.ID != "" {
				db, err := telemetry.OpenReadOnly(context.Background(), database)
				if err != nil {
					t.Fatal(err)
				}
				record, err := db.ReadApproval(context.Background(), reviewed.ID)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(record)
				if strings.Contains(string(body), secret) {
					t.Fatal("credential persisted in approval")
				}
				inspected, err := client.InspectApproval(context.Background(), reviewed.TaskID, reviewed.ID)
				if err != nil || inspected.State != record.State {
					t.Fatal("SDK approval inspection", inspected, err)
				}
				page, err := client.ListApprovals(context.Background(), approvals.ListOptions{TaskID: reviewed.TaskID, Limit: 1})
				if err != nil || page.Validate() != nil || len(page.Records) != 1 || page.Records[0].Request.ID != reviewed.ID || page.NextAfterCallID != "" {
					t.Fatal("SDK approval page", page, err)
				}
				if _, err = client.InspectApproval(context.Background(), "other", reviewed.ID); err == nil {
					t.Fatal("cross-task approval returned")
				}
			}
		})
	}
}

func TestSDKWriteRequiresReviewerAtConstruction(t *testing.T) {
	options, database := sdkToolOptions(t)
	options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "write", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: "scope", Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		t.Fatal("unexpected execution")
		return runtime.ToolResult{}, nil
	}}}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
	if _, err := sdk.New(options); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor touched storage", err)
	}
}

func TestSDKReviewedToolsCannotEnterDurableQueue(t *testing.T) {
	options, database := sdkToolOptions(t)
	options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "write", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: "scope", Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		t.Fatal("queued handler ran")
		return runtime.ToolResult{}, nil
	}}}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Ask}
	options.ApprovalReviewer = func(context.Context, sdk.ApprovalPrompt) (string, bool, error) {
		t.Fatal("queued approval ran")
		return "", false, nil
	}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Submit(context.Background(), "reviewed-tool-intake", sdk.Request{Version: 1, ModelID: "chat", Prompt: "write"}); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal("process-local authority queued", err)
	}
	if _, err = os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected queue touched storage", err)
	}
}

func TestSDKApprovalInspectionDoesNotCreateStorage(t *testing.T) {
	options, path := sdkToolOptions(t)
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		r, err := client.InspectApproval(ctx, "task", "approval")
		if err == nil || r.Request.ID != "" {
			t.Fatal("unexpected record", r, err)
		}
		p, err := client.ListApprovals(ctx, approvals.ListOptions{TaskID: "task", Limit: 25})
		if err == nil || p.Version != 0 {
			t.Fatal("unexpected page", p, err)
		}
	}
	var missing *sdk.Client
	if _, err = missing.InspectApproval(context.Background(), "task", "approval"); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if _, err = missing.ListApprovals(context.Background(), approvals.ListOptions{TaskID: "task", Limit: 25}); !errors.Is(err, sdk.ErrAdmission) {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created storage", err)
	}
}
