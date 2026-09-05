package toolgate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type fixtureModel func(context.Context, providers.Request, func(providers.Chunk) error) error

func (m fixtureModel) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return m(ctx, r, emit)
}
func (fixtureModel) Models(context.Context) ([]string, error) { return []string{"fixture"}, nil }

func TestDurableLoopApprovesWritesAndCompletes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := telemetry.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var reviewed approvals.Request
	g := &Gate{Store: s, Review: func(ctx context.Context, r approvals.Request) (string, bool, error) {
		events, err := s.Read(ctx, r.TaskID, 0, 100)
		if err != nil || events[len(events)-1].Kind != runtime.ToolStarted {
			t.Fatal("review before durable dispatch", err)
		}
		reviewed = r
		return "authenticated-test-operator", true, nil
	}}
	artifact := filepath.Join(dir, "artifact.txt")
	registry := &tools.Registry{}
	if err = registry.Register(tools.Definition{Tool: providers.Tool{Name: "write_artifact", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, Scope: "test-artifact", Handler: func(ctx context.Context, _ json.RawMessage) (runtime.ToolResult, error) {
		r, err := s.ReadApproval(ctx, reviewed.ID)
		if err != nil || r.State != approvals.Consumed {
			t.Fatal("handler ran before consumed approval", r, err)
		}
		leases, err := s.InspectLeases(ctx, "test-artifact")
		if err != nil || len(leases) != 1 || !leases[0].Writer {
			t.Fatal("handler lacks writer lease", leases, err)
		}
		if err = os.WriteFile(artifact, []byte("approved output"), 0600); err != nil {
			return runtime.ToolResult{Effect: runtime.UncertainEffect}, err
		}
		return runtime.ToolResult{Content: "artifact saved", Effect: runtime.ConfirmedEffect}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	turns := 0
	loop := runtime.Loop{Journal: s, Tools: tools.Executor{Registry: registry, Policy: &tools.Policy{Default: tools.Ask}, Authority: g}, Provider: fixtureModel(func(_ context.Context, r providers.Request, emit func(providers.Chunk) error) error {
		turns++
		if turns == 1 {
			if err := emit(providers.Chunk{ToolCall: &providers.ToolCall{ID: "call", Name: "write_artifact", Arguments: json.RawMessage(`{}`)}}); err != nil {
				return err
			}
			return emit(providers.Chunk{Done: true, FinishReason: "tool_calls"})
		}
		if len(r.Messages) != 3 || r.Messages[2].Content != "artifact saved" {
			t.Fatal("tool result not paired", r.Messages)
		}
		if err := emit(providers.Chunk{Text: "Completed approved artifact."}); err != nil {
			return err
		}
		return emit(providers.Chunk{Done: true, FinishReason: "stop"})
	})}
	result, err := loop.Run(ctx, runtime.RunRequest{TaskID: "task", SessionID: "session", ProviderID: "fixture", Inference: providers.Request{Model: "fixture", Messages: []providers.Message{{Role: "user", Content: "write artifact"}}, Tools: registry.Catalog()}, MaxTurns: 3, MaxOutputBytes: 4096})
	if err != nil || result.Text != "Completed approved artifact." || turns != 2 {
		t.Fatal(result, err, turns)
	}
	body, err := os.ReadFile(artifact)
	if err != nil || string(body) != "approved output" {
		t.Fatal(string(body), err)
	}
	snapshot, err := s.TaskSnapshot(ctx, "task")
	if err != nil || snapshot.State != "completed" || len(snapshot.Pending) != 0 {
		t.Fatal(snapshot, err)
	}
	leases, err := s.InspectLeases(ctx, "test-artifact")
	if err != nil || len(leases) != 0 {
		t.Fatal(leases, err)
	}
}
