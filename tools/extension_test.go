package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func extensionDefinition(name string) Definition {
	return Definition{Tool: providers.Tool{Name: name, Description: "Read trusted data", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Scope: "project", ReadOnly: true,
		Handler: func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
			return runtime.ToolResult{Content: `"ok"`, Effect: runtime.NoEffect}, nil
		}}
}

func TestExtensionRejectsInvalidUTF8Arguments(t *testing.T) {
	definition := extensionDefinition("lookup")
	definition.Tool.Parameters = json.RawMessage(`{"type":"object"}`)
	called := false
	definition.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		called = true
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	}
	extension, err := NewExtension([]Definition{definition}, &Policy{Default: Allow})
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{}
	if err := extension.RegisterInto(registry); err != nil {
		t.Fatal(err)
	}
	_, err = (Executor{Registry: registry, Policy: &Policy{Default: Allow}}).Execute(context.Background(), providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage("{\"x\":\"\xff\"}")})
	if !errors.Is(err, ErrArguments) || called {
		t.Fatal("invalid UTF-8 reached handler", err)
	}
}

func TestExtensionCompiledSchemasAcrossConcurrentRegistries(t *testing.T) {
	extension, err := NewExtension([]Definition{extensionDefinition("lookup")}, &Policy{Default: Allow})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			registry := &Registry{}
			if err := extension.RegisterInto(registry); err != nil {
				failures <- err
				return
			}
			executor := Executor{Registry: registry, Policy: &Policy{Rules: extension.Rules()}}
			for n := 0; n < 25; n++ {
				_, err := executor.Execute(context.Background(), providers.ToolCall{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{}`)})
				if err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestExtensionEmptyAndNil(t *testing.T) {
	for _, defs := range [][]Definition{nil, {}} {
		ext, err := NewExtension(defs, nil)
		if err != nil || ext != nil {
			t.Fatalf("empty = %v, %v", ext, err)
		}
	}
	var ext *Extension
	if len(ext.Catalog()) != 0 || len(ext.Names()) != 0 || len(ext.Rules()) != 0 {
		t.Fatal("nil extension exposes tools")
	}
	if err := ext.RegisterInto(&Registry{}); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionRejectsDefinitions(t *testing.T) {
	cases := map[string]func(*Definition){
		"empty name":          func(d *Definition) { d.Tool.Name = "" },
		"digit first":         func(d *Definition) { d.Tool.Name = "1tool" },
		"non ASCII":           func(d *Definition) { d.Tool.Name = "töol" },
		"punctuation":         func(d *Definition) { d.Tool.Name = "read-file" },
		"name length":         func(d *Definition) { d.Tool.Name = strings.Repeat("a", 65) },
		"empty scope":         func(d *Definition) { d.Scope = "" },
		"scope whitespace":    func(d *Definition) { d.Scope = " project" },
		"scope control":       func(d *Definition) { d.Scope = "project\nchild" },
		"scope wildcard":      func(d *Definition) { d.Scope = "project*" },
		"scope UTF8":          func(d *Definition) { d.Scope = string([]byte{255}) },
		"scope length":        func(d *Definition) { d.Scope = strings.Repeat("a", 257) },
		"description length":  func(d *Definition) { d.Tool.Description = strings.Repeat("a", 4097) },
		"description UTF8":    func(d *Definition) { d.Tool.Description = string([]byte{255}) },
		"schema length":       func(d *Definition) { d.Tool.Parameters = json.RawMessage(strings.Repeat(" ", 65537)) },
		"schema UTF8":         func(d *Definition) { d.Tool.Parameters = json.RawMessage{'{', 255, '}'} },
		"schema duplicate":    func(d *Definition) { d.Tool.Parameters = json.RawMessage(`{"type":"object","type":"string"}`) },
		"schema external ref": func(d *Definition) { d.Tool.Parameters = json.RawMessage(`{"$ref":"https://example.invalid/schema"}`) },
		"schema invalid":      func(d *Definition) { d.Tool.Parameters = json.RawMessage(`{"type":"not-a-type"}`) },
		"schema array":        func(d *Definition) { d.Tool.Parameters = json.RawMessage(`[]`) },
		"effecting":           func(d *Definition) { d.ReadOnly = false },
		"nil handler":         func(d *Definition) { d.Handler = nil },
	}
	for _, name := range []string{"read_file", "delegate", "delegate_batch"} {
		cases["reserved "+name] = func(d *Definition) { d.Tool.Name = name }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := extensionDefinition("custom")
			mutate(&d)
			if ext, err := NewExtension([]Definition{d}, &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) || ext != nil {
				t.Fatalf("accepted invalid definition: %v, %v", ext, err)
			}
		})
	}
}

func TestExtensionCatalogBounds(t *testing.T) {
	defs := make([]Definition, 16)
	for i := range defs {
		defs[i] = extensionDefinition(fmt.Sprintf("tool_%d", i))
	}
	if _, err := NewExtension(defs, &Policy{Default: Allow}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExtension(append(defs, extensionDefinition("extra")), &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("17 tools: %v", err)
	}
	if _, err := NewExtension([]Definition{defs[0], defs[0]}, &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("duplicate: %v", err)
	}
	for i := range defs {
		defs[i].Tool.Parameters = json.RawMessage(`{"type":"object","description":"` + strings.Repeat("a", 20<<10) + `"}`)
	}
	if _, err := NewExtension(defs, &Policy{Default: Allow}); !errors.Is(err, ErrDefinition) {
		t.Fatalf("aggregate catalog: %v", err)
	}
	d := extensionDefinition(strings.Repeat("a", 64))
	d.Scope = strings.Repeat("s", 256)
	d.Tool.Description = strings.Repeat("d", 4096)
	d.Tool.Parameters = json.RawMessage(`{"type":"object"}` + strings.Repeat(" ", (64<<10)-len(`{"type":"object"}`)))
	if _, err := NewExtension([]Definition{d}, &Policy{Default: Allow}); err != nil {
		t.Fatalf("exact field bounds: %v", err)
	}
}

func TestExtensionResolvedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  *Policy
		exposed bool
	}{
		{"nil", nil, false}, {"default empty", &Policy{}, false}, {"default deny", &Policy{Default: Deny}, false},
		{"default ask", &Policy{Default: Ask}, false}, {"default allow", &Policy{Default: Allow}, true},
		{"inherited deny", &Policy{Default: Allow, Parent: &Policy{Default: Deny}}, false},
		{"inherited ask", &Policy{Default: Allow, Parent: &Policy{Default: Ask}}, false},
		{"explicit allow", &Policy{Rules: []Rule{{Tool: "custom", Scope: "project", Decision: Allow}}}, true},
		{"wildcard allow", &Policy{Rules: []Rule{{Tool: "*", Scope: "*", Decision: Allow}}}, true},
		{"explicit deny wins", &Policy{Default: Allow, Rules: []Rule{{Tool: "custom", Scope: "project", Decision: Deny}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext, err := NewExtension([]Definition{extensionDefinition("custom")}, tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.exposed {
				want = 1
			}
			if len(ext.Names()) != want || len(ext.Catalog()) != want || len(ext.Rules()) != want {
				t.Fatalf("unexpected exposure: %v %v", ext.Names(), ext.Rules())
			}
			reg := &Registry{}
			if err := ext.RegisterInto(reg); err != nil {
				t.Fatal(err)
			}
			if len(reg.Catalog()) != want {
				t.Fatal("registration differs from catalog")
			}
			if tc.exposed {
				if !reflect.DeepEqual(ext.Rules(), []Rule{{Tool: "custom", Scope: "project", Decision: Allow}}) {
					t.Fatalf("authority not exact: %v", ext.Rules())
				}
				p := &Policy{Rules: ext.Rules()}
				if p.Decide("read_file", "project") != Deny || p.Decide("custom", "elsewhere") != Deny {
					t.Fatal("authority escaped extension")
				}
			}
		})
	}
}

func TestExtensionPolicyBoundsAndCycles(t *testing.T) {
	cycle := &Policy{Default: Allow}
	cycle.Parent = cycle
	deep := &Policy{Default: Allow}
	for i := 0; i < 40; i++ {
		deep = &Policy{Default: Allow, Parent: deep}
	}
	many := &Policy{Default: Allow, Rules: make([]Rule, 257)}
	for i := range many.Rules {
		many.Rules[i] = Rule{Tool: "*", Scope: "*", Decision: Allow}
	}
	indirectCycle := &Policy{Default: Allow, Parent: &Policy{Default: Allow}}
	indirectCycle.Parent.Parent = indirectCycle
	distributed := &Policy{Default: Allow, Rules: append([]Rule(nil), many.Rules[:128]...), Parent: &Policy{Default: Allow, Rules: append([]Rule(nil), many.Rules[128:]...)}}
	for name, p := range map[string]*Policy{"cycle": cycle, "indirect cycle": indirectCycle, "deep": deep, "rules": many, "distributed rules": distributed, "default": {Default: "invalid"}, "decision": {Rules: []Rule{{Tool: "*", Scope: "*", Decision: "invalid"}}}} {
		t.Run(name, func(t *testing.T) {
			if ext, err := NewExtension([]Definition{extensionDefinition("custom")}, p); !errors.Is(err, ErrDefinition) || ext != nil {
				t.Fatalf("invalid policy accepted: %v %v", ext, err)
			}
		})
	}
}

func TestExtensionSnapshots(t *testing.T) {
	defs := []Definition{extensionDefinition("custom")}
	p := &Policy{Rules: []Rule{{Tool: "custom", Scope: "project", Decision: Allow}}, Parent: &Policy{Default: Allow}}
	ext, err := NewExtension(defs, p)
	if err != nil {
		t.Fatal(err)
	}
	defs[0].Tool.Name = "changed"
	defs[0].Tool.Parameters[0] = '['
	defs[0].Handler = nil
	p.Rules[0].Decision = Deny
	p.Parent.Default = Deny
	catalog := ext.Catalog()
	catalog[0].Name = "changed"
	catalog[0].Parameters[0] = '['
	names := ext.Names()
	names[0] = "changed"
	rules := ext.Rules()
	rules[0].Decision = Deny
	if ext.Names()[0] != "custom" || ext.Catalog()[0].Parameters[0] != '{' || ext.Rules()[0].Decision != Allow {
		t.Fatal("mutable extension snapshot")
	}
	reg := &Registry{}
	if err := ext.RegisterInto(reg); err != nil {
		t.Fatal(err)
	}
	out, err := (Executor{Registry: reg, Policy: &Policy{Rules: ext.Rules()}}).Execute(context.Background(), providers.ToolCall{ID: "call", Name: "custom", Arguments: json.RawMessage(`{}`)})
	if err != nil || string(out.Content) != `"ok"` {
		t.Fatalf("snapshot execution: %s %v", out.Content, err)
	}
}

func TestExtensionDeniedHandlerNeverCalled(t *testing.T) {
	called := false
	d := extensionDefinition("custom")
	d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		called = true
		return runtime.ToolResult{Effect: runtime.NoEffect}, nil
	}
	ext, err := NewExtension([]Definition{d}, &Policy{})
	if err != nil {
		t.Fatal(err)
	}
	reg := &Registry{}
	if err := ext.RegisterInto(reg); err != nil {
		t.Fatal(err)
	}
	_, err = (Executor{Registry: reg, Policy: &Policy{Default: Allow}}).Execute(context.Background(), providers.ToolCall{ID: "call", Name: "custom", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrDenied) || called {
		t.Fatalf("denied extension dispatched: %v called=%v", err, called)
	}
}

func TestExtensionHandlerUTF8Boundary(t *testing.T) {
	d := extensionDefinition("custom")
	d.Handler = func(context.Context, json.RawMessage) (runtime.ToolResult, error) {
		return runtime.ToolResult{Content: string([]byte{255}), Effect: runtime.NoEffect}, nil
	}
	ext, err := NewExtension([]Definition{d}, &Policy{Default: Allow})
	if err != nil {
		t.Fatal(err)
	}
	reg := &Registry{}
	if err := ext.RegisterInto(reg); err != nil {
		t.Fatal(err)
	}
	out, err := (Executor{Registry: reg, Policy: &Policy{Rules: ext.Rules()}}).Execute(context.Background(), providers.ToolCall{ID: "call", Name: "custom", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrExecution) || out.Content != "" || out.Effect != runtime.UncertainEffect {
		t.Fatalf("invalid output admitted: %#v %v", out, err)
	}
}

func TestExtensionRegisterCollisionIsAtomic(t *testing.T) {
	ext, err := NewExtension([]Definition{extensionDefinition("a_custom"), extensionDefinition("z_existing")}, &Policy{Default: Allow})
	if err != nil {
		t.Fatal(err)
	}
	reg := &Registry{}
	if err := reg.Register(extensionDefinition("z_existing")); err != nil {
		t.Fatal(err)
	}
	if err := ext.RegisterInto(reg); !errors.Is(err, ErrDefinition) {
		t.Fatalf("collision accepted: %v", err)
	}
	if got := reg.Catalog(); len(got) != 1 || got[0].Name != "z_existing" {
		t.Fatalf("partially registered: %v", got)
	}
	if err := ext.RegisterInto(nil); !errors.Is(err, ErrDefinition) {
		t.Fatalf("nil target: %v", err)
	}
}
