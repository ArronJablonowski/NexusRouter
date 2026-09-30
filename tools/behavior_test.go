package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestToolBehaviorRegistrationAndSnapshots(t *testing.T) {
	for _, tc := range []struct {
		read           bool
		declared, want Behavior
		invalid        bool
	}{
		{true, "", BehaviorReadOnly, false}, {false, "", BehaviorNonIdempotentWrite, false},
		{true, BehaviorReadOnly, BehaviorReadOnly, false},
		{false, BehaviorIdempotentWrite, BehaviorIdempotentWrite, false},
		{false, BehaviorNonIdempotentWrite, BehaviorNonIdempotentWrite, false},
		{false, BehaviorReadOnly, "", true}, {true, BehaviorIdempotentWrite, "", true},
		{true, BehaviorNonIdempotentWrite, "", true}, {false, "unknown", "", true},
	} {
		t.Run(fmt.Sprint(tc.read, tc.declared), func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.ReadOnly, d.Behavior = tc.read, tc.declared
			r := &Registry{}
			err := r.Register(d)
			if tc.invalid {
				if !errors.Is(err, ErrDefinition) {
					t.Fatal("invalid behavior accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d.Behavior = "changed"
			got := r.Descriptions()
			if len(got) != 1 || got[0].Behavior != tc.want || (Executor{Registry: r}).ToolBehavior("lookup") != tc.want {
				t.Fatal(got)
			}
			got[0].Behavior = "changed"
			if r.Descriptions()[0].Behavior != tc.want {
				t.Fatal("mutable description")
			}
			if (Executor{Registry: r}).ToolBehavior("missing") != "" {
				t.Fatal("unknown tool classified")
			}
		})
	}
}

func TestBehaviorExtensionInspectionAndWriteAdmission(t *testing.T) {
	calls := 0
	a, b := definition(&calls), definition(&calls)
	a.Tool.Name, b.Tool.Name = "z", "a"
	b.ReadOnly, b.Behavior = false, BehaviorIdempotentWrite
	if _, err := NewExtension([]Definition{b}, &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) {
		t.Fatal("unreviewed write admitted")
	}
	e, err := NewApprovalExtension([]Definition{a, b}, &Policy{Default: Allow})
	if err != nil {
		t.Fatal(err)
	}
	a.Behavior, b.Behavior = "changed", "changed"
	descriptions := e.Descriptions()
	if len(descriptions) != 2 || descriptions[0].Name != "a" || descriptions[0].Behavior != BehaviorIdempotentWrite || descriptions[1].Behavior != BehaviorReadOnly || !e.RequiresApproval() {
		t.Fatal(descriptions)
	}
	descriptions[0].Name = "changed"
	r := &Registry{}
	if e.RegisterInto(r) != nil || r.Descriptions()[0].Name != "a" {
		t.Fatal("snapshot changed")
	}
	body, _ := json.Marshal(e.Catalog())
	var catalog []map[string]json.RawMessage
	if json.Unmarshal(body, &catalog) != nil {
		t.Fatal("catalog invalid")
	}
	for _, item := range catalog {
		if item["behavior"] != nil {
			t.Fatal("behavior leaked onto provider schema")
		}
	}
}

func TestWriteBehaviorApprovalBindingAndSingleUse(t *testing.T) {
	digests := map[Behavior]string{}
	for _, behavior := range []Behavior{BehaviorIdempotentWrite, BehaviorNonIdempotentWrite} {
		t.Run(string(behavior), func(t *testing.T) {
			calls := 0
			d := definition(&calls)
			d.ReadOnly, d.Behavior = false, behavior
			d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
				calls++
				return runtime.ToolResult{Content: "effect", Effect: runtime.ConfirmedEffect}, nil
			}
			r := &Registry{}
			if err := r.Register(d); err != nil {
				t.Fatal(err)
			}
			e := Executor{Registry: r, Policy: &Policy{Default: Allow}}
			if _, err := e.ExecuteScoped(context.Background(), authorizedExecution()); !errors.Is(err, ErrDenied) || calls != 0 {
				t.Fatal("write without authority")
			}
			e.Authority = authorityFunc(func(ctx context.Context, a Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
				if a.ToolBehavior != behavior {
					t.Fatal("approval behavior missing")
				}
				digests[behavior] = a.PolicyDigest
				result, err := invoke(ctx)
				if _, again := invoke(ctx); !errors.Is(again, ErrDenied) {
					t.Fatal("approval callback repeated")
				}
				return result, err
			})
			if _, err := e.Execute(context.Background(), authorizedExecution().Call); !errors.Is(err, ErrDenied) || calls != 0 {
				t.Fatal("unscoped write")
			}
			out, err := e.ExecuteScoped(context.Background(), authorizedExecution())
			if err != nil || calls != 1 || out.Effect != runtime.ConfirmedEffect {
				t.Fatal(out, err, calls)
			}
		})
	}
	if digests[BehaviorIdempotentWrite] == "" || digests[BehaviorIdempotentWrite] == digests[BehaviorNonIdempotentWrite] {
		t.Fatal("behavior not bound to policy digest")
	}
}

func TestIdempotentDeclarationDoesNotDowngradeUncertainty(t *testing.T) {
	calls := 0
	d := definition(&calls)
	d.ReadOnly, d.Behavior = false, BehaviorIdempotentWrite
	d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		calls++
		return runtime.ToolResult{Effect: runtime.NoEffect}, errors.New("fixture error")
	}
	r := &Registry{}
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	e := Executor{Registry: r, Policy: &Policy{Default: Allow}, Authority: authorityFunc(func(ctx context.Context, _ Authorization, invoke func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
		return invoke(ctx)
	})}
	out, err := e.ExecuteScoped(context.Background(), authorizedExecution())
	if !errors.Is(err, ErrExecution) || out.Effect != runtime.UncertainEffect || out.Recoverable || calls != 1 {
		t.Fatal("idempotency declaration changed uncertain outcome", out, err, calls)
	}
}
