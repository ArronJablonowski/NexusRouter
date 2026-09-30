package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestDelegateToolsInheritancePreservesDenial(t *testing.T) {
	registry := &tools.Registry{}
	allow := &tools.Policy{Default: tools.Allow}
	for _, test := range []struct {
		registry *tools.Registry
		policy   *tools.Policy
	}{
		{nil, allow}, {registry, nil},
		{registry, &tools.Policy{Default: tools.Allow, Rules: []tools.Rule{{Tool: "read_file", Scope: "workspace", Decision: tools.Deny}}}},
		{registry, &tools.Policy{Default: tools.Allow, Rules: []tools.Rule{{Tool: "read_file", Scope: "workspace", Decision: tools.Ask}}}},
		{registry, &tools.Policy{Default: tools.Allow, Parent: &tools.Policy{Default: tools.Deny}}},
		{registry, &tools.Policy{Default: tools.Allow, Parent: &tools.Policy{Default: tools.Ask}}},
	} {
		ctx, err := inheritDelegateTools(context.Background(), test.registry, test.policy)
		if !errors.Is(err, ErrAdmission) || ctx.Value(delegateToolsKey{}) != nil {
			t.Fatal("unsafe capability issued", err)
		}
	}
	ctx, err := inheritDelegateTools(context.Background(), registry, allow)
	if err != nil {
		t.Fatal(err)
	}
	capability := ctx.Value(delegateToolsKey{}).(*delegateTools)
	if capability.Registry != registry || capability.Policy.Decide("read_file", "workspace") != tools.Allow {
		t.Fatal("read capability not inherited")
	}
	for _, identity := range [][2]string{{"delegate", "delegation"}, {"delegate_batch", "delegation"}, {"write_file", "workspace"}, {"read_file", "other"}} {
		if capability.Policy.Decide(identity[0], identity[1]) != tools.Deny {
			t.Fatal("child escalated", identity)
		}
	}
}

func TestDelegateToolsBorrowedRootSurvivesPathReplacement(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	moved := filepath.Join(dir, "original")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fact.txt"), []byte("original trusted binding"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, closeRoot, err := readTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoot()
	ctx, err := inheritDelegateTools(context.Background(), registry, applicationToolPolicy())
	if err != nil {
		t.Fatal(err)
	}
	capability := ctx.Value(delegateToolsKey{}).(*delegateTools)
	if err = os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "fact.txt"), []byte("replacement attacker path"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := tools.Executor{Registry: capability.Registry, Policy: capability.Policy}
	out, err := executor.Execute(ctx, providers.ToolCall{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"fact.txt"}`)})
	if err != nil || !strings.Contains(out.Content, "original trusted binding") || strings.Contains(out.Content, "replacement") {
		t.Fatal(out, err)
	}
	if err = os.Symlink(filepath.Join(root, "fact.txt"), filepath.Join(moved, "escape")); err != nil {
		t.Fatal(err)
	}
	out, err = executor.Execute(ctx, providers.ToolCall{ID: "escape", Name: "read_file", Arguments: json.RawMessage(`{"path":"escape"}`)})
	if err != nil || strings.Contains(out.Content, "replacement") || !strings.Contains(out.Content, "file_unavailable") {
		t.Fatal("escaped borrowed root", out, err)
	}
}

func TestDelegateToolsExplicitlyDenyUnadvertisedBorrowedWrites(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fact.txt"), []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, closeRoot, err := readTools(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoot()
	called := 0
	for _, name := range []string{"workboard_create_card", "extension_write"} {
		if err := registry.Register(tools.Definition{Tool: providers.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
			Scope: "borrowed", Behavior: runtime.BehaviorIdempotentWrite, Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				called++
				return runtime.ToolResult{Effect: runtime.ConfirmedEffect}, nil
			}}); err != nil {
			t.Fatal(err)
		}
	}
	parent := &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{
		{Tool: "read_file", Scope: "workspace", Decision: tools.Allow},
		{Tool: "workboard_create_card", Scope: "*", Decision: tools.Ask},
		{Tool: "extension_write", Scope: "*", Decision: tools.Ask},
	}}
	ctx, err := inheritDelegateTools(context.Background(), registry, parent)
	if err != nil {
		t.Fatal(err)
	}
	capability := ctx.Value(delegateToolsKey{}).(*delegateTools)
	for _, name := range []string{"workboard_create_card", "extension_write"} {
		if capability.Policy.Decide(name, "borrowed") != tools.Deny {
			t.Fatalf("parent Ask widened child denial for %s", name)
		}
		out, executeErr := (tools.Executor{Registry: capability.Registry, Policy: capability.Policy}).Execute(ctx,
			providers.ToolCall{ID: "unadvertised", Name: name, Arguments: json.RawMessage(`{}`)})
		if !errors.Is(executeErr, tools.ErrDenied) || out.Effect != runtime.NoEffect {
			t.Fatalf("unadvertised %s execution: out=%+v error=%v", name, out, executeErr)
		}
	}
	if called != 0 {
		t.Fatalf("denied borrowed handlers executed %d times", called)
	}
	out, err := (tools.Executor{Registry: capability.Registry, Policy: capability.Policy}).Execute(ctx,
		providers.ToolCall{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"fact.txt"}`)})
	if err != nil || !strings.Contains(out.Content, "trusted") {
		t.Fatalf("allowed borrowed read failed: out=%+v error=%v", out, err)
	}
}

func TestDelegateToolsMissingCapabilityRejectsBeforeAdmission(t *testing.T) {
	cfg := config.Defaults()
	cfg.Workers.DelegateModel = "child"
	cfg.Workers.DelegateReadTools = true
	cfg.Tools.Enabled = true
	cfg.Tools.ReadRoot = t.TempDir()
	zero := 0.0
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	cfg.Models = []config.Model{{ID: "child", Provider: "local", Model: "child", Locality: "local", RAMBytes: 1, ContextTokens: 8192, EstimatedCost: &zero, Capabilities: []string{"chat"}}}
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "missing.db")
	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	profiled := false
	service.profile = func(context.Context) (resources.Snapshot, error) {
		profiled = true
		return resources.Snapshot{}, resources.ErrProfile
	}
	out, err := service.runDelegate(context.Background(), "prompt", "", "work", true, "", "")
	if !errors.Is(err, ErrAdmission) || out.TaskID != "" || len(service.execution) != 0 || profiled {
		t.Fatal(out, err)
	}
	if _, err = os.Stat(cfg.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing capability created database", err)
	}
}

func TestApplicationToolPolicyUsesConfiguredWriteDecision(t *testing.T) {
	for _, test := range []struct {
		configured string
		want       tools.Decision
	}{
		{"deny", tools.Deny},
		{"ask", tools.Ask},
		{"allow", tools.Allow},
		{"invalid", tools.Deny},
	} {
		policy := applicationToolPolicyFor(test.configured)
		for _, target := range []struct{ tool, scope string }{
			{"workboard_create_board", "workboards"},
			{"workboard_revise_board", "workboard:board_a"},
			{"workboard_archive_board", "workboard:board_a"},
			{"workboard_create_card", "workboard:board_a"},
			{"workboard_update_card", "workboard:board_a"},
			{"workboard_transition_card", "workboard:board_a"},
			{"workboard_reorder_card", "workboard:board_a"},
			{"workboard_add_dependency", "workboard:board_a"},
			{"workboard_remove_dependency", "workboard:board_a"},
			{"workboard_request_pause", "workboard:board_a"},
			{"workboard_request_resume", "workboard:board_a"},
			{"workboard_request_cancel", "workboard:board_a"},
			{"workboard_propose_criteria", "workboard:board_a"},
			{"workboard_request_candidate_decision", "workboard:board_a"},
		} {
			if got := policy.Decide(target.tool, target.scope); got != test.want {
				t.Fatalf("configured=%q tool=%q scope=%q got=%q want=%q", test.configured, target.tool, target.scope, got, test.want)
			}
		}
		if got := policy.Decide("workboard_read", "workboard:board_a"); got != tools.Allow {
			t.Fatalf("configured=%q changed read decision to %q", test.configured, got)
		}
	}
}
