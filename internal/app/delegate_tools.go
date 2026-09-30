package app

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

type delegateToolsKey struct{}

// This capability is private, process-local and scoped to a live parent call.
// Its registry borrows the parent's already-open filesystem root. A child never
// reopens a pathname that may have changed since the parent was admitted.
type delegateTools struct {
	Registry *tools.Registry
	Policy   *tools.Policy
}

func delegatedReadCatalog(registry *tools.Registry) []providers.Tool {
	if registry == nil {
		return nil
	}
	for _, candidate := range registry.Catalog() {
		if candidate.Name == "read_file" {
			return []providers.Tool{candidate}
		}
	}
	return nil
}

func inheritDelegateTools(ctx context.Context, registry *tools.Registry, parent *tools.Policy) (context.Context, error) {
	if registry == nil || parent == nil || parent.Decide("read_file", "workspace") != tools.Allow {
		return ctx, ErrAdmission
	}
	rules := []tools.Rule{{Tool: "read_file", Scope: "workspace", Decision: tools.Allow}}
	// The registry is borrowed from the root and can contain approved writes.
	// Explicit child denials prevent a parent Ask rule from widening the child's
	// deny-by-default policy if a model emits an unadvertised tool name.
	for _, definition := range registry.Catalog() {
		if definition.Name != "read_file" {
			rules = append(rules, tools.Rule{Tool: definition.Name, Scope: "*", Decision: tools.Deny})
		}
	}
	policy := &tools.Policy{Default: tools.Deny, Parent: parent, Rules: rules}
	return context.WithValue(ctx, delegateToolsKey{}, &delegateTools{Registry: registry, Policy: policy}), nil
}

func applicationToolPolicy() *tools.Policy {
	return applicationToolPolicyFor("ask")
}

func applicationToolPolicyFor(configured string) *tools.Policy {
	writeDecision := tools.Deny
	switch configured {
	case "allow":
		writeDecision = tools.Allow
	case "ask":
		writeDecision = tools.Ask
	}
	return &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{
		{Tool: "read_file", Scope: "workspace", Decision: tools.Allow},
		{Tool: "workboard_list", Scope: "workboards", Decision: tools.Allow},
		{Tool: "workboard_read", Scope: "*", Decision: tools.Allow},
		{Tool: "workboard_create_board", Scope: "workboards", Decision: writeDecision},
		{Tool: "workboard_revise_board", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_archive_board", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_create_card", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_update_card", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_transition_card", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_reorder_card", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_add_dependency", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_remove_dependency", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_request_pause", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_request_resume", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_request_cancel", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_propose_criteria", Scope: "*", Decision: writeDecision},
		{Tool: "workboard_request_candidate_decision", Scope: "*", Decision: writeDecision},
		{Tool: "delegate", Scope: "delegation", Decision: tools.Allow},
		{Tool: "delegate_batch", Scope: "delegation", Decision: tools.Allow},
	}}
}
