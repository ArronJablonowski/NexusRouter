package tools

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// Extension is an immutable set of tools admitted by an explicit host policy.
// Handlers are trusted host code: the registry cannot sandbox effects, network
// access, or mutable closure state. The host owns cancellation and concurrency.
// Extension policy applies only to these definitions, never to built-in tools.
type Extension struct{ entries []extensionEntry }

type extensionEntry struct {
	entry
	decision Decision
}

// NewExtension compiles schemas and resolves a bounded snapshot of inherited
// policy. Ask is not approval, so only Allow tools enter the executable catalog.
// Unsupported write tools and reserved runtime identities fail at construction.
func NewExtension(definitions []Definition, policy *Policy) (*Extension, error) {
	return newExtension(definitions, policy, false)
}

// NewApprovalExtension admits effective Allow and Ask tools, including writes.
// It does not grant execution authority: writes and Ask require a scoped
// Authority at dispatch. Inherited denials are never relaxed.
func NewApprovalExtension(definitions []Definition, policy *Policy) (*Extension, error) {
	return newExtension(definitions, policy, true)
}

func newExtension(definitions []Definition, policy *Policy, reviewed bool) (*Extension, error) {
	if len(definitions) == 0 {
		return nil, nil
	}
	if len(definitions) > 16 {
		return nil, ErrDefinition
	}
	snapshot, err := extensionPolicy(policy)
	if err != nil {
		return nil, err
	}
	registry := &Registry{}
	total := 0
	for _, d := range definitions {
		if !extensionName(d.Tool.Name) || !extensionScope(d.Scope) || d.ResolveScope != nil || (!reviewed && !d.ReadOnly) || d.Handler == nil || !utf8.ValidString(d.Tool.Description) || len(d.Tool.Description) > 4096 || !utf8.Valid(d.Tool.Parameters) || len(d.Tool.Parameters) > 64<<10 {
			return nil, ErrDefinition
		}
		switch d.Tool.Name {
		case "read_file", "create_file", "replace_file", "workboard_list", "workboard_read",
			"workboard_create_board", "workboard_revise_board", "workboard_archive_board", "workboard_create_card", "workboard_update_card", "workboard_transition_card",
			"workboard_add_dependency", "workboard_remove_dependency", "workboard_reorder_card", "workboard_request_pause", "workboard_request_cancel", "delegate", "delegate_batch":
			return nil, ErrDefinition
		}
		body, err := json.Marshal(d.Tool)
		if err != nil || len(body) > (256<<10)-total {
			return nil, ErrDefinition
		}
		total += len(body)
		if registry.Register(d) != nil {
			return nil, ErrDefinition
		}
	}
	out := &Extension{}
	for _, e := range registry.entries {
		decision := snapshot.Decide(e.Tool.Name, e.Scope)
		if decision == Allow || (reviewed && decision == Ask) {
			out.entries = append(out.entries, extensionEntry{entry: e, decision: decision})
		}
	}
	sort.Slice(out.entries, func(i, j int) bool { return out.entries[i].Tool.Name < out.entries[j].Tool.Name })
	return out, nil
}

func (e *Extension) Catalog() []providers.Tool {
	if e == nil {
		return nil
	}
	out := make([]providers.Tool, 0, len(e.entries))
	for _, item := range e.entries {
		tool := item.Tool
		tool.Parameters = append(json.RawMessage(nil), tool.Parameters...)
		out = append(out, tool)
	}
	return out
}

func (e *Extension) Names() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.entries))
	for _, item := range e.entries {
		out = append(out, item.Tool.Name)
	}
	return out
}

func (e *Extension) Rules() []Rule {
	if e == nil {
		return nil
	}
	out := make([]Rule, 0, len(e.entries))
	for _, item := range e.entries {
		out = append(out, Rule{Tool: item.Tool.Name, Scope: item.Scope, Decision: item.decision})
	}
	return out
}

// RequiresApproval reports whether any admitted tool writes or requires Ask.
func (e *Extension) RequiresApproval() bool {
	if e != nil {
		for _, item := range e.entries {
			if !item.ReadOnly || item.decision == Ask {
				return true
			}
		}
	}
	return false
}

// RegisterInto is atomic with respect to identity collisions. Compiled schemas
// are immutable and shared; externally visible schema bytes remain isolated.
func (e *Extension) RegisterInto(registry *Registry) error {
	if e == nil {
		return nil
	}
	if registry == nil {
		return ErrDefinition
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, item := range e.entries {
		if _, exists := registry.entries[item.Tool.Name]; exists {
			return ErrDefinition
		}
	}
	if registry.entries == nil {
		registry.entries = map[string]entry{}
	}
	for _, item := range e.entries {
		item.Tool.Parameters = append(json.RawMessage(nil), item.Tool.Parameters...)
		registry.entries[item.Tool.Name] = item.entry
	}
	return nil
}

func extensionName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i, c := range []byte(s) {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func extensionScope(s string) bool {
	return len(s) > 0 && len(s) <= 256 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsAny(s, "*") && !strings.ContainsFunc(s, unicode.IsControl)
}

func extensionPolicy(policy *Policy) (*Policy, error) {
	var first, previous *Policy
	seen := map[*Policy]bool{}
	total := 0
	validDecision := func(d Decision) bool { return d == Allow || d == Deny || d == Ask }
	for p := policy; p != nil; p = p.Parent {
		if seen[p] || len(seen) >= 32 || (p.Default != "" && !validDecision(p.Default)) || len(p.Rules) > 256-total {
			return nil, ErrDefinition
		}
		seen[p] = true
		total += len(p.Rules)
		copy := &Policy{Default: p.Default, Rules: append([]Rule(nil), p.Rules...)}
		for _, rule := range copy.Rules {
			if (rule.Tool != "*" && !extensionName(rule.Tool)) || (rule.Scope != "*" && !extensionScope(rule.Scope)) || !validDecision(rule.Decision) {
				return nil, ErrDefinition
			}
		}
		if first == nil {
			first = copy
		} else {
			previous.Parent = copy
		}
		previous = copy
	}
	return first, nil
}
