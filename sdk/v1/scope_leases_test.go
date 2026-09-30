package v1_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestSDKScopeLeaseObservationReadsOverlappingLegacyHolders(t *testing.T) {
	for _, mode := range []string{"live_reader", "expired_reader", "live_writer", "expired_writer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			options, path := sdkToolOptions(t)
			var client *sdk.Client
			reviewed := false
			options.Tools = []sdk.Tool{{Tool: providers.Tool{Name: "write_evidence", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "workspace", Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				t.Error("inspection dispatched writer")
				return runtime.ToolResult{}, nil
			}}}
			options.ToolPolicy = &sdk.ToolPolicy{Default: tools.Ask}
			options.ApprovalReviewer = func(ctx context.Context, p sdk.ApprovalPrompt) (string, bool, error) {
				reviewed = true
				db, err := telemetry.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				now := time.Now().UTC()
				if strings.HasPrefix(mode, "expired") {
					now = now.Add(-time.Minute)
				}
				lease, err := db.AcquireLease(ctx, p.Request.TaskID, "private-holder-owner", "create_private-legacy", strings.HasSuffix(mode, "writer"), now, 30*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				before, err := db.Read(ctx, p.Request.TaskID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				record, err := db.ReadApproval(ctx, p.Request.ID)
				if err != nil {
					t.Fatal(err)
				}
				leases, err := db.InspectLeases(ctx, "create_private-legacy")
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					status, err := client.ApprovalExecutionStatus(ctx, p.Request.TaskID, p.Request.ID)
					want := approvals.ScopeLeaseObservation{Version: 1, OverlapPolicyVersion: 1}
					switch mode {
					case "live_reader":
						want.LiveReaders = 1
					case "expired_reader":
						want.ExpiredReaders = 1
					case "live_writer":
						want.LiveWriters = 1
					case "expired_writer":
						want.ExpiredWriters = 1
					}
					if err != nil || status.Validate() != nil || status.ScopeLeases == nil || *status.ScopeLeases != want || status.ScopeWriterState != "none" {
						t.Fatal("incorrect namespace observation", status, err)
					}
					body, _ := json.Marshal(status)
					for _, private := range []string{lease.Token, lease.Owner, lease.Scope, `"token"`, `"arguments":`} {
						if strings.Contains(string(body), private) {
							t.Fatal("raw lease capability disclosed")
						}
					}
				}
				after, err := db.Read(ctx, p.Request.TaskID, 0, 100)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("journal changed", err)
				}
				afterRecord, err := db.ReadApproval(ctx, p.Request.ID)
				if err != nil || !reflect.DeepEqual(record, afterRecord) {
					t.Fatal("approval changed", err)
				}
				afterLeases, err := db.InspectLeases(ctx, "create_private-legacy")
				if err != nil || !reflect.DeepEqual(leases, afterLeases) {
					t.Fatal("lease changed", err)
				}
				return "operator", false, nil
			}
			options.ProviderFactory = sdkProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return sdkProviderStream(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
					if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "write_evidence", Arguments: json.RawMessage(`{}`)}}); err != nil {
						return err
					}
					return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
				}), nil
			})
			var err error
			client, err = sdk.New(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Run(ctx, sdk.Request{Version: 1, ModelID: "chat", Prompt: "inspect scoped writer"}); err == nil || !reviewed {
				t.Fatal("expected denied reviewed task", err)
			}
		})
	}
}
