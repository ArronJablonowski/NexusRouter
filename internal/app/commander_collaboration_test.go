package app

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"path/filepath"
	"testing"
)

type fakeCommanderBridge struct {
	calls, tokens int
	private       bool
}

func (f *fakeCommanderBridge) List(context.Context, int, bool) ([]CommanderPeer, error) {
	return nil, nil
}
func (f *fakeCommanderBridge) Consult(_ context.Context, _, _, _, _ string, tokens int, private bool) (Result, error) {
	f.calls++
	f.tokens = tokens
	f.private = private
	return Result{TaskID: "child", Text: "answer"}, nil
}
func TestCommanderBindingPreservesScope(t *testing.T) {
	f := &fakeCommanderBridge{}
	s := &Service{}
	ctx := context.WithValue(context.Background(), commanderInstanceKey{}, "peer")
	r := Request{collaboration: f, ContextTokens: 128}
	out, err := s.bindDelegate(r)(ctx, "assignment", "text", "work", true)
	if err != nil || out.Text != "answer" || f.calls != 1 || f.tokens != 128 || !f.private {
		t.Fatal("scope not preserved")
	}
	r.RemoteExecution = &runtime.RemoteExecution{Mode: "consult", Depth: 1}
	if _, err = s.bindDelegate(r)(ctx, "assignment", "text", "work", true); err == nil || f.calls != 1 {
		t.Fatal("remote recursion allowed")
	}
	if _, err = s.bindDelegate(Request{})(ctx, "assignment", "text", "work", true); err == nil {
		t.Fatal("unbound caller allowed")
	}
}

func TestCommanderToolsAndSharedBudget(t *testing.T) {
	ctx := context.Background()
	f := &fakeCommanderBridge{}
	reg := &tools.Registry{}
	if err := registerCommanderList(reg, f, 128, true); err != nil {
		t.Fatal(err)
	}
	out, err := (tools.Executor{Registry: reg, Policy: applicationToolPolicy()}).Execute(ctx, providers.ToolCall{ID: "list", Name: "list_commanders", Arguments: json.RawMessage(`{}`)})
	if err != nil || out.Effect != runtime.NoEffect {
		t.Fatal("list tool failed", err)
	}
	db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{}
	runner := s.bindDelegate(Request{collaboration: f, ContextTokens: 128})
	executor := delegateSafetyExecutor(t, db, db, runner)
	call := delegateSafetyCall(`{"instance_id":"peer","prompt":"assignment","validation":"text"}`)
	out, err = executor.Execute(ctx, call)
	if err != nil || out.Failed || f.calls != 1 {
		t.Fatal("remote delegation failed", err, out.Failed)
	}
	call.ID = "second"
	out, err = executor.Execute(ctx, call)
	if err != nil || !out.Failed || f.calls != 1 {
		t.Fatal("shared budget exceeded", err)
	}
}
