package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

const delegationCompactionPolicyVersion = 1

// delegationCompactionPolicy is the finite permission surface a delegated
// child can receive. It is embedded in the durable summary-policy snapshot and
// copied into each plan-bound worker start. The snapshots are evidence, not a
// replacement for the live tool executor's policy check.
type delegationCompactionPolicy struct {
	Version   int                  `json:"version"`
	Enabled   bool                 `json:"enabled"`
	ReadTools bool                 `json:"read_tools"`
	Parent    tools.PolicySnapshot `json:"parent"`
	Child     tools.PolicySnapshot `json:"child"`
}

func sealDelegationCompactionPolicy(settings config.Settings) (delegationCompactionPolicy, error) {
	points := []tools.PolicyPoint{
		{Tool: "delegate", Scope: "delegation"},
		{Tool: "delegate_batch", Scope: "delegation"},
		{Tool: "read_file", Scope: "workspace"},
	}
	parent, err := tools.SealPolicySnapshot(applicationToolPolicyFor(settings.Security.ToolPolicy), points)
	if err != nil {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	childPolicy := &tools.Policy{Default: tools.Deny, Rules: []tools.Rule{
		{Tool: "delegate", Scope: "delegation", Decision: tools.Deny},
		{Tool: "delegate_batch", Scope: "delegation", Decision: tools.Deny},
		{Tool: "read_file", Scope: "workspace", Decision: tools.Deny},
	}}
	if settings.Workers.DelegateReadTools {
		childPolicy.Parent = applicationToolPolicyFor(settings.Security.ToolPolicy)
		childPolicy.Rules[2].Decision = tools.Allow
	}
	child, err := tools.SealPolicySnapshot(childPolicy, points)
	if err != nil || !child.AtMost(parent) {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	out := delegationCompactionPolicy{
		Version: delegationCompactionPolicyVersion, Enabled: settings.Workers.DelegateModel != "",
		ReadTools: settings.Workers.DelegateReadTools, Parent: parent, Child: child,
	}
	if out.validate() != nil {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	return out, nil
}

func (p delegationCompactionPolicy) validate() error {
	if p.Version != delegationCompactionPolicyVersion || p.Parent.Validate() != nil || p.Child.Validate() != nil || !p.Child.AtMost(p.Parent) || p.ReadTools && !p.Enabled {
		return ErrAdmission
	}
	expected := []tools.PolicyPoint{
		{Tool: "delegate", Scope: "delegation"},
		{Tool: "delegate_batch", Scope: "delegation"},
		{Tool: "read_file", Scope: "workspace"},
	}
	if len(p.Parent.Entries) != len(expected) || len(p.Child.Entries) != len(expected) {
		return ErrAdmission
	}
	read := tools.Deny
	for index, entry := range p.Child.Entries {
		if entry.Tool != expected[index].Tool || entry.Scope != expected[index].Scope ||
			p.Parent.Entries[index].Tool != expected[index].Tool || p.Parent.Entries[index].Scope != expected[index].Scope {
			return ErrAdmission
		}
		if entry.Tool == "read_file" && entry.Scope == "workspace" {
			read = entry.Decision
		}
		if (entry.Tool == "delegate" || entry.Tool == "delegate_batch") && entry.Decision != tools.Deny {
			return ErrAdmission
		}
	}
	if p.ReadTools != (read == tools.Allow) {
		return ErrAdmission
	}
	return nil
}

func (p delegationCompactionPolicy) canonicalJSON() (json.RawMessage, error) {
	if p.validate() != nil {
		return nil, ErrAdmission
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, ErrAdmission
	}
	return body, nil
}

func delegationCompactionPolicyFromSnapshot(raw json.RawMessage) (delegationCompactionPolicy, error) {
	var snapshot summaryPreparationPolicySnapshot
	if len(raw) == 0 || json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != prepareSummaryVersion || snapshot.Delegation.validate() != nil {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil || decoder.Decode(new(any)) != io.EOF {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, raw) {
		return delegationCompactionPolicy{}, ErrAdmission
	}
	return snapshot.Delegation, nil
}

func (s *Service) delegationCompactionPolicyForPlan(ctx context.Context, read *telemetry.Store, plan *runtime.ContextCompactionPlan) (*delegationCompactionPolicy, error) {
	if plan == nil {
		return nil, nil
	}
	state, err := read.ContextCompactionPlan(ctx, plan.OperationID)
	if err != nil || state.Plan == nil || !reflect.DeepEqual(*state.Plan, *plan) || state.Start.PolicyDigest != plan.PolicyDigest {
		return nil, ErrAdmission
	}
	frozen, err := delegationCompactionPolicyFromSnapshot(state.Start.PolicySnapshot)
	if err != nil {
		return nil, err
	}
	current, err := sealDelegationCompactionPolicy(s.settings)
	if err != nil || !reflect.DeepEqual(current, frozen) {
		return nil, ErrAdmission
	}
	return &frozen, nil
}

func sealDelegationCompactionAuthority(parent string, plan *runtime.ContextCompactionPlan, policy *delegationCompactionPolicy) (*runtime.DelegationCompactionAuthority, error) {
	if plan == nil {
		if policy != nil {
			return nil, ErrAdmission
		}
		return nil, nil
	}
	if policy == nil || policy.validate() != nil {
		return nil, ErrAdmission
	}
	parentPolicy, err := policy.Parent.CanonicalJSON()
	if err != nil {
		return nil, ErrAdmission
	}
	childPolicy, err := policy.Child.CanonicalJSON()
	if err != nil {
		return nil, ErrAdmission
	}
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID: parent, PlanDigest: plan.PlanDigest, InheritedEngineDigest: plan.Engine.Digest,
		Scope: "delegation-" + parent, ParentPolicy: parentPolicy, ChildPolicy: childPolicy,
	})
	if err != nil {
		return nil, ErrAdmission
	}
	return &authority, nil
}
