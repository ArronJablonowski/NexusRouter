package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestApprovalExtensionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
		policy   *Policy
		decision Decision
		needs    bool
	}{
		{"read allow", true, &Policy{Default: Allow}, Allow, false},
		{"write allow", false, &Policy{Default: Allow}, Allow, true},
		{"read ask", true, &Policy{Default: Ask}, Ask, true},
		{"write ask", false, &Policy{Default: Ask}, Ask, true},
		{"inherited ask", false, &Policy{Default: Allow, Parent: &Policy{Default: Ask}}, Ask, true},
		{"inherited deny", false, &Policy{Default: Allow, Parent: &Policy{Default: Deny}}, Deny, false},
		{"nil policy", false, nil, Deny, false},
		{"explicit deny", false, &Policy{Default: Ask, Rules: []Rule{{Tool: "lookup", Scope: "project", Decision: Deny}}}, Deny, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.ReadOnly = tc.readOnly
			ext, err := NewApprovalExtension([]Definition{d}, tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.decision == Deny {
				want = 0
			}
			if len(ext.Catalog()) != want || len(ext.Names()) != want || len(ext.Rules()) != want || ext.RequiresApproval() != tc.needs {
				t.Fatalf("incorrect admission: %v %v needs=%v", ext.Names(), ext.Rules(), ext.RequiresApproval())
			}
			if want == 1 && !reflect.DeepEqual(ext.Rules(), []Rule{{Tool: "lookup", Scope: "project", Decision: tc.decision}}) {
				t.Fatal("decision changed", ext.Rules())
			}
			registry := &Registry{}
			if err := ext.RegisterInto(registry); err != nil {
				t.Fatal(err)
			}
			executor := Executor{Registry: registry, Policy: &Policy{Rules: ext.Rules()}}
			_, err = executor.ExecuteScoped(context.Background(), authorizedExecution())
			if tc.decision == Deny || tc.needs {
				if !errors.Is(err, ErrDenied) || calls != 0 {
					t.Fatal("unapproved tool dispatched", err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatal("read-only allow changed", err, calls)
			}
		})
	}
	var ext *Extension
	if ext.RequiresApproval() {
		t.Fatal("nil needs approval")
	}
	if ext, err := NewApprovalExtension(nil, nil); err != nil || ext != nil {
		t.Fatal("empty extension changed", ext, err)
	}
}

func TestApprovalExtensionSnapshotAndReviewPreviewIsolation(t *testing.T) {
	calls := 0
	d := definition(&calls)
	d.ReadOnly = false
	d.Tool.Description = "Write the proposed value"
	x := authorizedExecution()
	original := append(json.RawMessage(nil), x.Call.Arguments...)
	d.Handler = func(_ context.Context, args json.RawMessage) (runtime.ToolResult, error) {
		calls++
		if !bytes.Equal(args, original) {
			t.Error("review changed executed arguments")
		}
		return runtime.ToolResult{Content: "written", Effect: runtime.ConfirmedEffect}, nil
	}
	policy := &Policy{Default: Allow, Parent: &Policy{Default: Ask}}
	ext, err := NewApprovalExtension([]Definition{d}, policy)
	if err != nil {
		t.Fatal(err)
	}
	d.Tool.Parameters[0] = '['
	d.Tool.Description = "changed"
	d.Handler = nil
	policy.Parent.Default = Deny
	catalog := ext.Catalog()
	catalog[0].Description = "changed"
	catalog[0].Parameters[0] = '['
	rules := ext.Rules()
	rules[0].Decision = Allow
	names := ext.Names()
	names[0] = "changed"
	if ext.Rules()[0].Decision != Ask || ext.Catalog()[0].Description != "Write the proposed value" || ext.Catalog()[0].Parameters[0] != '{' || ext.Names()[0] != "lookup" {
		t.Fatal("mutable approval extension")
	}
	registry := &Registry{}
	if err := ext.RegisterInto(registry); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: registry, Policy: &Policy{Rules: ext.Rules()}, Authority: authorityFunc(func(ctx context.Context, a Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		if !bytes.Equal(a.Arguments, original) || a.Description != "Write the proposed value" {
			t.Fatal("wrong preview")
		}
		body, err := json.Marshal(a)
		if err != nil || bytes.Contains(body, []byte("value")) || bytes.Contains(body, []byte("Write the proposed value")) {
			t.Fatal("ephemeral preview serialized", string(body), err)
		}
		a.Arguments[0] = '['
		return invoke(ctx)
	})}
	out, err := e.ExecuteScoped(context.Background(), x)
	if err != nil || out.Content != "written" || out.Effect != runtime.ConfirmedEffect || calls != 1 || !bytes.Equal(x.Call.Arguments, original) {
		t.Fatalf("isolated execution failed: %+v %v calls=%d", out, err, calls)
	}
	previewJSON, err := json.Marshal(ApprovalPrompt{Arguments: original, Description: "private description"})
	if err != nil || bytes.Contains(previewJSON, []byte("value")) || bytes.Contains(previewJSON, []byte("private description")) {
		t.Fatal("prompt preview serialized", string(previewJSON), err)
	}
}

func TestApprovalExtensionRetainsDefinitionBounds(t *testing.T) {
	for _, mutate := range []func(*Definition){
		func(d *Definition) { d.Tool.Name = "read_file" },
		func(d *Definition) { d.Tool.Name = "delegate" },
		func(d *Definition) { d.Tool.Name = "delegate_batch" },
		func(d *Definition) { d.Scope = "*" },
		func(d *Definition) { d.Tool.Name = "bad-name" },
		func(d *Definition) { d.Tool.Parameters = json.RawMessage(`{"$ref":"https://example.invalid/schema"}`) },
		func(d *Definition) { d.Tool.Parameters = json.RawMessage(`{"type":"object","type":"string"}`) },
		func(d *Definition) { d.Handler = nil },
	} {
		d := extensionDefinition("custom")
		d.ReadOnly = false
		mutate(&d)
		if ext, err := NewApprovalExtension([]Definition{d}, &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) || ext != nil {
			t.Fatal("invalid reviewed definition admitted", err)
		}
	}
}
