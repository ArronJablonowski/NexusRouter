// Package tools implements the schema and authorization boundary for tools.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrDenied = errors.New("tool denied")
var ErrArguments = errors.New("invalid tool arguments")
var ErrDefinition = errors.New("invalid tool definition")
var ErrExecution = errors.New("tool execution failed")

type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Ask   Decision = "ask"
)

// Rules use exact tool and resource-scope identities; '*' matches everything.
// Scope is registered by trusted code, never selected by model arguments.
type Rule struct {
	Tool, Scope string
	Decision    Decision
}
type Policy struct {
	Default Decision
	Rules   []Rule
	Parent  *Policy
}

func (p *Policy) Decide(tool, scope string) Decision {
	if p == nil {
		return Deny
	}
	d := p.Default
	if d != Allow && d != Ask {
		d = Deny
	}
	matched := false
	for _, r := range p.Rules {
		if (r.Tool == tool || r.Tool == "*") && (r.Scope == scope || r.Scope == "*") {
			if r.Decision != Allow && r.Decision != Ask {
				return Deny
			}
			if !matched {
				d = r.Decision
				matched = true
			} else if r.Decision == Ask {
				d = Ask
			}
		}
	}
	if p.Parent != nil {
		parent := p.Parent.Decide(tool, scope)
		if parent == Deny {
			return Deny
		}
		if parent == Ask {
			d = Ask
		}
	}
	return d
}

type Definition struct {
	Tool     providers.Tool
	Scope    string
	ReadOnly bool
	Handler  func(context.Context, json.RawMessage) (runtime.ToolResult, error)
}
type entry struct {
	Definition
	schema *jsonschema.Schema
}

// Register during startup. Entries are immutable snapshots after registration.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
}
type denyLoader struct{}

func (denyLoader) Load(string) (any, error) { return nil, ErrDefinition }

func (r *Registry) Register(d Definition) error {
	if d.Tool.Name == "" || d.Scope == "" || d.Handler == nil {
		return ErrDefinition
	}
	v, err := decode(d.Tool.Parameters)
	if err != nil {
		return ErrDefinition
	}
	if _, ok := v.(map[string]any); !ok {
		return ErrDefinition
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(denyLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	const location = "https://darwin.invalid/tool-schema"
	if c.AddResource(location, v) != nil {
		return ErrDefinition
	}
	schema, err := c.Compile(location)
	if err != nil {
		return ErrDefinition
	}
	d.Tool.Parameters = append(json.RawMessage(nil), d.Tool.Parameters...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]entry{}
	}
	if _, ok := r.entries[d.Tool.Name]; ok {
		return ErrDefinition
	}
	r.entries[d.Tool.Name] = entry{d, schema}
	return nil
}
func (r *Registry) Catalog() []providers.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []providers.Tool{}
	for _, e := range r.entries {
		t := e.Tool
		t.Parameters = append(json.RawMessage(nil), t.Parameters...)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Executor admits writes or Ask only through an explicit scoped Authority.
// Policy must remain immutable for the task lifetime. Application extensions
// still reject writes until the operator approval surface is integrated.
type Executor struct {
	Registry  *Registry
	Policy    *Policy
	Authority Authority
}

func (e Executor) Execute(ctx context.Context, call providers.ToolCall) (out runtime.ToolResult, err error) {
	return e.execute(ctx, runtime.ToolExecution{Call: call}, false)
}

func (e Executor) ExecuteScoped(ctx context.Context, execution runtime.ToolExecution) (runtime.ToolResult, error) {
	return e.execute(ctx, execution, true)
}

func (e Executor) execute(ctx context.Context, execution runtime.ToolExecution, scoped bool) (out runtime.ToolResult, err error) {
	call := execution.Call
	out.Effect = runtime.NoEffect
	if ctx == nil {
		return out, ErrDenied
	}
	// Mask a previous handler's identity before any nested execution path.
	ctx = context.WithValue(ctx, executionIdentityKey{}, ExecutionIdentity{})
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if e.Registry == nil || call.ID == "" {
		return out, ErrDenied
	}
	e.Registry.mu.RLock()
	t, ok := e.Registry.entries[call.Name]
	e.Registry.mu.RUnlock()
	policy, policyErr := extensionPolicy(e.Policy)
	if !ok || policyErr != nil {
		return out, ErrDenied
	}
	decision := policy.Decide(call.Name, t.Scope)
	needsApproval := !t.ReadOnly || decision == Ask
	if decision == Deny || (needsApproval && (!scoped || e.Authority == nil)) {
		return out, ErrDenied
	}
	arguments := append(json.RawMessage(nil), call.Arguments...)
	v, decodeErr := decode(arguments)
	if decodeErr != nil {
		return out, ErrArguments
	}
	if _, ok := v.(map[string]any); !ok {
		return out, ErrArguments
	}
	if t.schema.Validate(v) != nil {
		return out, ErrArguments
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if scoped {
		identity := ExecutionIdentity{TaskID: execution.TaskID, SessionID: execution.SessionID, TurnID: execution.TurnID, AttemptID: execution.AttemptID, ToolCallID: call.ID, ToolName: call.Name}
		if !identity.valid() {
			return out, ErrDenied
		}
		ctx = context.WithValue(ctx, executionIdentityKey{}, identity)
	}
	if needsApproval {
		return e.approved(ctx, execution, t, policy, arguments)
	}
	defer func() {
		if recover() != nil {
			out = runtime.ToolResult{Effect: runtime.UncertainEffect}
			err = ErrExecution
		}
	}()
	out, err = t.Handler(ctx, arguments)
	if err != nil || out.Effect != runtime.NoEffect || (out.Recoverable && !out.Failed) || len(out.Content) > 1<<20 || !utf8.ValidString(out.Content) {
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, ErrExecution
	}
	return out, nil
}

// Reject duplicate keys rather than allowing validator/handler disagreement.
func decode(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !utf8.Valid(raw) {
		return nil, ErrArguments
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) (any, error)
	value = func(depth int) (any, error) {
		if depth > 64 {
			return nil, ErrArguments
		}
		token, err := d.Token()
		if err != nil {
			return nil, ErrArguments
		}
		switch token {
		case json.Delim('{'):
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, ErrArguments
				}
				k, ok := key.(string)
				if !ok {
					return nil, ErrArguments
				}
				if _, exists := m[k]; exists {
					return nil, ErrArguments
				}
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrArguments
			}
			return m, nil
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrArguments
			}
			return a, nil
		default:
			if _, ok := token.(json.Delim); ok {
				return nil, ErrArguments
			}
			return token, nil
		}
	}
	v, err := value(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrArguments
	}
	return v, nil
}
