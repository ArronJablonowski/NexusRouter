package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
	"github.com/ArronJablonowski/DarwinRouter/tools"
	"go.yaml.in/yaml/v3"
)

func TestSDKDeclaredWriterBehaviorAcrossRealHTTPAndApproval(t *testing.T) {
	for _, behavior := range []sdk.ToolBehavior{sdk.BehaviorIdempotentWrite, sdk.BehaviorNonIdempotentWrite} {
		for _, mode := range []string{"success", "uncertain", "confirmed_provider_failure"} {
			t.Run(string(behavior)+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				var streams, effects, reviews, fallbackCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/tags" {
						fmt.Fprintln(w, `{"models":[{"name":"fixture"},{"name":"z-fixture"}]}`)
						return
					}
					var request struct {
						Model string `json:"model"`
					}
					if json.NewDecoder(r.Body).Decode(&request) != nil {
						http.Error(w, "fixture", 400)
						return
					}
					if request.Model == "z-fixture" {
						fallbackCalls.Add(1)
					}
					if streams.Add(1) == 1 {
						fmt.Fprintln(w, `{"message":{"tool_calls":[{"function":{"name":"save_exact","arguments":{"content":"exact approved bytes"}}}]},"done":true,"done_reason":"tool_calls"}`)
						return
					}
					if mode == "confirmed_provider_failure" {
						fmt.Fprintln(w, `{"error":"fixture stream failed"}`)
						return
					}
					fmt.Fprintln(w, `{"message":{"content":"saved"},"done":true,"done_reason":"stop"}`)
				}))
				defer server.Close()
				options, _ := sdkToolOptions(t)
				body, err := os.ReadFile(options.ProjectFile)
				if err != nil {
					t.Fatal(err)
				}
				var cfg config.Settings
				if err = yaml.Unmarshal(body, &cfg); err != nil {
					t.Fatal(err)
				}
				cfg.Providers[0].Endpoint = server.URL
				cfg.Routing.Exploration = 0
				fallback := cfg.Models[0]
				fallback.ID = "z"
				fallback.Model = "z-fixture"
				cfg.Models = append(cfg.Models, fallback)
				body, err = yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(options.ProjectFile, body, 0600); err != nil {
					t.Fatal(err)
				}
				artifact := filepath.Join(t.TempDir(), "exact.txt")
				var client *sdk.Client
				var reviewed approvals.Request
				options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "save_exact", Description: "Save exact operator-approved bytes", Parameters: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"}},"required":["content"],"additionalProperties":false}`)}, Scope: "exact-artifact", Behavior: behavior, Handler: func(ctx context.Context, args json.RawMessage) (runtime.ToolResult, error) {
					status, err := client.ApprovalExecutionStatus(ctx, reviewed.TaskID, reviewed.ID)
					if err != nil || status.Approval.State != approvals.Consumed || status.Approval.Request.ToolBehavior != behavior || status.ScopeWriterState != "live" {
						t.Error("handler lacked exact-class approval and writer lease")
						return runtime.ToolResult{Effect: runtime.UncertainEffect}, errors.New("fixture")
					}
					var input struct {
						Content string `json:"content"`
					}
					if json.Unmarshal(args, &input) != nil {
						return runtime.ToolResult{Effect: runtime.NoEffect, Failed: true}, nil
					}
					effects.Add(1)
					if err = os.WriteFile(artifact, []byte(input.Content), 0600); err != nil {
						return runtime.ToolResult{Effect: runtime.UncertainEffect}, err
					}
					if mode == "uncertain" {
						return runtime.ToolResult{Content: "uncertain fixture result", Effect: runtime.UncertainEffect}, nil
					}
					return runtime.ToolResult{Content: "saved", Effect: runtime.ConfirmedEffect}, nil
				}}}
				options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
				options.ApprovalReviewer = func(ctx context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
					reviews.Add(1)
					reviewed = p.Request
					if p.Request.ToolBehavior != behavior {
						t.Error("review omitted declared behavior")
					}
					status, err := client.ApprovalExecutionStatus(ctx, p.Request.TaskID, p.Request.ID)
					if err != nil || status.Approval.Request.ToolBehavior != behavior || status.Approval.State != approvals.Pending || status.ScopeWriterState != "none" {
						t.Error("preview class not durable before authority")
					}
					return "fixture-operator", true, nil
				}
				client, err = sdk.New(options)
				if err != nil {
					t.Fatal(err)
				}
				result, runErr := client.Run(ctx, sdk.Request{Version: 1, ModelID: "auto", Prompt: "save exact bytes"})
				if (runErr == nil) != (mode == "success") || effects.Load() != 1 || reviews.Load() != 1 || fallbackCalls.Load() != 0 {
					t.Fatal("declared behavior bypassed authority or replayed effect", runErr, effects.Load(), reviews.Load(), fallbackCalls.Load())
				}
				wantStreams := int32(2)
				if mode == "uncertain" {
					wantStreams = 1
				}
				if streams.Load() != wantStreams {
					t.Fatal("tool class granted inference retry", streams.Load())
				}
				body, err = os.ReadFile(artifact)
				if err != nil || string(body) != "exact approved bytes" {
					t.Fatal("approved effect missing or changed", err)
				}
				record, err := client.InspectApproval(ctx, reviewed.TaskID, reviewed.ID)
				if err != nil || record.Request.ToolBehavior != behavior || record.State != approvals.Consumed {
					t.Fatal("durable behavior missing", err)
				}
				page, err := client.ReadEvents(ctx, reviewed.TaskID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				starts, completions := 0, 0
				for _, event := range page.Events {
					if event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted {
						if event.Data.ToolBehavior != behavior {
							t.Fatal("history lost declared operation class")
						}
						if event.Kind == runtime.ToolStarted {
							starts++
						} else {
							completions++
						}
					}
				}
				if starts != 1 || completions != 1 {
					t.Fatal("unexpected effect history", starts, completions)
				}
				if mode == "success" && result.TaskID != reviewed.TaskID {
					t.Fatal("approval task mismatch")
				}
			})
		}
	}
}

func TestSDKDeclaredBehaviorRequiresAuthorityAndCompatibleReadOnly(t *testing.T) {
	for _, tc := range []struct {
		behavior sdk.ToolBehavior
		readOnly bool
	}{{sdk.BehaviorIdempotentWrite, false}, {sdk.BehaviorNonIdempotentWrite, false}, {sdk.BehaviorIdempotentWrite, true}, {sdk.BehaviorNonIdempotentWrite, true}, {sdk.BehaviorReadOnly, false}, {sdk.ToolBehavior("invented"), true}} {
		options, database := sdkToolOptions(t)
		options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "operation", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: "artifact", ReadOnly: tc.readOnly, Behavior: tc.behavior, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			t.Error("unadmitted handler executed")
			return runtime.ToolResult{}, nil
		}}}
		options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
		if tc.readOnly || tc.behavior == sdk.BehaviorReadOnly {
			// Supplying a reviewer must not rescue a contradictory declaration.
			options.ApprovalReviewer = func(context.Context, sdk.ApprovalPrompt) (string, bool, error) { return "fixture-operator", true, nil }
		}
		if client, err := sdk.New(options); !errors.Is(err, sdk.ErrAdmission) || client != nil {
			t.Fatal("behavior accepted without compatible authority", tc, err)
		}
		if _, err := os.Stat(database); !os.IsNotExist(err) {
			t.Fatal("denied behavior created storage")
		}
	}
}

func TestSDKExplicitReadOnlyBehaviorNeedsNoWriteAuthority(t *testing.T) {
	options, database := sdkToolOptions(t)
	options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "inspect", Parameters: json.RawMessage(`{"type":"object"}`)}, Scope: "public", ReadOnly: true, Behavior: sdk.BehaviorReadOnly, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	}}}
	options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Allow}
	if client, err := sdk.New(options); err != nil || client == nil {
		t.Fatal("explicit readonly declaration rejected", err)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("read-only construction created storage")
	}
}
