package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func applicationApprovalExtension(t *testing.T, calls *atomic.Int32) *tools.Extension {
	t.Helper()
	ext, err := tools.NewApprovalExtension([]tools.Definition{{Tool: providers.Tool{Name: "lookup", Description: "Write trusted evidence", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "fixture", Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		calls.Add(1)
		return runtime.ToolResult{Content: "trusted evidence", Effect: runtime.ConfirmedEffect}, nil
	}}}, &tools.Policy{Default: tools.Ask})
	if err != nil {
		t.Fatal(err)
	}
	return ext
}

func TestAppApprovalExplicitAutomaticStreamsAndChildIsolation(t *testing.T) {
	for _, mode := range []string{"explicit", "automatic", "events", "text", "child"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Workers.DelegateModel = "z"
			var calls, reviews, streams atomic.Int32
			catalog := []string{"delegate", "delegate_batch", "lookup"}
			if mode == "child" {
				catalog = []string{}
			}
			factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
				return extensionProvider{t: t, wantTools: catalog, streams: &streams}, nil
			})
			var reviewed approvals.Request
			reviewer := func(_ context.Context, p tools.ApprovalPrompt) (string, bool, error) {
				reviews.Add(1)
				if string(p.Arguments) != "{}" || p.Description != "Write trusted evidence" || p.Request.ToolName != "lookup" {
					t.Error("incorrect operator preview", p)
				}
				reviewed = p.Request
				return "operator", true, nil
			}
			svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, factory, applicationApprovalExtension(t, &calls), reviewer)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			request := Request{ModelID: "a", Prompt: "inspect"}
			var result Result
			var events []runtime.Kind
			var text string
			switch mode {
			case "automatic":
				request.ModelID = "auto"
				result, err = svc.Run(context.Background(), request)
			case "events":
				result, err = svc.RunStream(context.Background(), request, func(e runtime.Event) error { events = append(events, e.Kind); return nil })
			case "text":
				result, err = svc.RunTextStream(context.Background(), request, func(chunk string) error { text += chunk; return nil })
			case "child":
				result, err = svc.runDelegate(context.Background(), "inspect", "", "parent-work", true, "", "")
			default:
				result, err = svc.Run(context.Background(), request)
			}
			wantCalls, wantStreams := int32(1), int32(2)
			if mode == "child" {
				wantCalls, wantStreams = 0, 1
			}
			if err != nil || result.Text != "final answer" || calls.Load() != wantCalls || reviews.Load() != wantCalls || streams.Load() != wantStreams {
				t.Fatalf("result=%+v err=%v calls=%d reviews=%d streams=%d", result, err, calls.Load(), reviews.Load(), streams.Load())
			}
			if mode == "events" && (!slices.Contains(events, runtime.ToolCompleted) || events[len(events)-1] != runtime.TaskCompleted) {
				t.Fatal(events)
			}
			if mode == "text" && text != "final answer" {
				t.Fatal(text)
			}
			db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			snapshot, err := db.TaskSnapshot(context.Background(), result.TaskID)
			if err != nil || snapshot.State != "completed" || snapshot.Privacy != "local_only" {
				t.Fatal(snapshot, err)
			}
			if mode != "child" {
				record, err := db.ReadApproval(context.Background(), reviewed.ID)
				if err != nil || record.State != approvals.Consumed {
					t.Fatal(record, err)
				}
			}
		})
	}
}

type unadvertisedWriteProvider struct{ t *testing.T }

func (unadvertisedWriteProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (p unadvertisedWriteProvider) Stream(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	if len(r.Tools) != 0 {
		p.t.Error("child received parent tools", r.Tools)
	}
	if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "unexpected-write", Name: "lookup", Arguments: json.RawMessage(`{}`)}}); err != nil {
		return err
	}
	return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
}

func TestAppApprovalChildCannotInvokeUnadvertisedWrite(t *testing.T) {
	fixture, cfg := autoFixture(t)
	cfg.Workers.DelegateModel = "z"
	var calls, reviews atomic.Int32
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		return unadvertisedWriteProvider{t: t}, nil
	})
	svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, factory, applicationApprovalExtension(t, &calls), func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		reviews.Add(1)
		return "operator", true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	if _, err = svc.runDelegate(context.Background(), "inspect", "", "parent-work", true, "", ""); err == nil {
		t.Fatal("unadvertised child write accepted")
	}
	if calls.Load() != 0 || reviews.Load() != 0 {
		t.Fatal(calls.Load(), reviews.Load())
	}
}

func TestAppApprovalRequiresReviewerAndRejectsDurableAuthority(t *testing.T) {
	fixture, cfg := autoFixture(t)
	var calls, builds, reviews atomic.Int32
	ext := applicationApprovalExtension(t, &calls)
	factory := applicationProviderFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, nil
	})
	if svc, err := NewServiceWithToolExtension(cfg, nil, nil, nil, nil, factory, ext); err != ErrAdmission || svc != nil {
		t.Fatal("legacy constructor accepted write authority", svc, err)
	}
	svc, err := NewServiceWithToolApproval(cfg, nil, nil, nil, nil, factory, ext, func(context.Context, tools.ApprovalPrompt) (string, bool, error) {
		reviews.Add(1)
		return "operator", true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = fixture.profile
	ctx := context.Background()
	request := Request{ModelID: "a", Prompt: "inspect"}
	if _, err = svc.Submit(ctx, "approval-intake-key", request); err != ErrAdmission {
		t.Fatal(err)
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("denied intake touched storage", err)
	}
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
		t.Fatal(result, err)
	}
	after, err := db.Submission(ctx, status.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("denied run mutated submission", before, after, err)
	}
	if calls.Load() != 0 || reviews.Load() != 0 || builds.Load() != 0 {
		t.Fatal(calls.Load(), reviews.Load(), builds.Load())
	}
}
