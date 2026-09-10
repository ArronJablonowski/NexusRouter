package app

import (
	"context"

	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type delegateToolsKey struct{}

// This capability is private, process-local and scoped to a live parent call.
// Its registry borrows the parent's already-open filesystem root. A child never
// reopens a pathname that may have changed since the parent was admitted.
type delegateTools struct {
	Registry *tools.Registry
	Policy   *tools.Policy
}

func inheritDelegateTools(ctx context.Context, registry *tools.Registry, parent *tools.Policy) (context.Context, error) {
	if registry == nil || parent == nil || parent.Decide("read_file", "workspace") != tools.Allow {
		return ctx, ErrAdmission
	}
	policy := &tools.Policy{Default: tools.Deny, Parent: parent, Rules: []tools.Rule{{Tool: "read_file", Scope: "workspace", Decision: tools.Allow}}}
	return context.WithValue(ctx, delegateToolsKey{}, &delegateTools{Registry: registry, Policy: policy}), nil
}

func applicationToolPolicy() *tools.Policy {
	return &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{
		{Tool: "read_file", Scope: "workspace", Decision: tools.Allow},
		{Tool: "workboard_list", Scope: "workboards", Decision: tools.Allow},
		{Tool: "workboard_read", Scope: "workboards", Decision: tools.Allow},
		{Tool: "delegate", Scope: "delegation", Decision: tools.Allow},
		{Tool: "delegate_batch", Scope: "delegation", Decision: tools.Allow},
	}}
}
