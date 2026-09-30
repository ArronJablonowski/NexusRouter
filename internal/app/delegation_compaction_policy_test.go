package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestDelegationCompactionPolicyDeterministicAndSummaryBound(t *testing.T) {
	ctx := context.Background()
	svc, _ := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "summary policy source"})
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	first, err := svc.prepareSummaryAdmission(ctx, "delegation-policy-deterministic-0001", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.prepareSummaryAdmission(ctx, "delegation-policy-deterministic-0001", request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.policy, second.policy) || summaryPreparationDigest(first.policy) != summaryPreparationDigest(second.policy) {
		t.Fatal("equivalent summary policy was not deterministic")
	}
	var snapshot summaryPreparationPolicySnapshot
	if json.Unmarshal(first.policy, &snapshot) != nil || snapshot.Delegation.validate() != nil {
		t.Fatal("summary policy omitted valid delegation evidence", string(first.policy))
	}
	delegationJSON, err := snapshot.Delegation.canonicalJSON()
	embedded, encodeErr := json.Marshal(snapshot.Delegation)
	if err != nil || encodeErr != nil || string(embedded) != string(delegationJSON) {
		t.Fatal("durable summary policy does not bind canonical delegation evidence", string(first.policy), string(delegationJSON), err, encodeErr)
	}
}

func TestDelegationCompactionPolicyChildAuthorityReadToolsOnAndOff(t *testing.T) {
	for _, readTools := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[readTools], func(t *testing.T) {
			settings := config.Defaults()
			settings.Workers.DelegateModel = "worker"
			settings.Workers.DelegateReadTools = readTools
			policy, err := sealDelegationCompactionPolicy(settings)
			if err != nil || policy.validate() != nil || !policy.Child.AtMost(policy.Parent) || !policy.Enabled || policy.ReadTools != readTools {
				t.Fatal(policy, err)
			}
			parent := policyPoints(policy.Parent)
			child := policyPoints(policy.Child)
			if parent["delegate/delegation"] != tools.Allow || parent["delegate_batch/delegation"] != tools.Allow || parent["read_file/workspace"] != tools.Allow {
				t.Fatal("unexpected parent authority", parent)
			}
			if child["delegate/delegation"] != tools.Deny || child["delegate_batch/delegation"] != tools.Deny {
				t.Fatal("child acquired recursive delegation", child)
			}
			wantRead := tools.Deny
			if readTools {
				wantRead = tools.Allow
			}
			if child["read_file/workspace"] != wantRead {
				t.Fatal("child read authority does not match configuration", child)
			}
		})
	}
}

func TestDelegationCompactionPolicyConfigDriftChangesSummaryPolicyDigest(t *testing.T) {
	ctx := context.Background()
	svc, _ := autoFixture(t)
	source, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "summary policy drift source"})
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareSummaryRequest{Version: 1, TaskID: source.TaskID, ModelID: "a", Keep: 1, MaxCost: 0}
	baseline, err := svc.prepareSummaryAdmission(ctx, "delegation-policy-drift-base-0001", request)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := summaryPreparationDigest(baseline.policy)
	for name, drift := range map[string]func(*config.Settings){
		"enable delegation": func(s *config.Settings) { s.Workers.DelegateModel = "a" },
		"enable read tools": func(s *config.Settings) {
			s.Workers.DelegateModel = "a"
			s.Workers.DelegateReadTools = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			original := svc.settings
			defer func() { svc.settings = original }()
			drift(&svc.settings)
			changed, prepareErr := svc.prepareSummaryAdmission(ctx, "delegation-policy-drift-"+strings.ReplaceAll(name, " ", "-")+"-0001", request)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			if summaryPreparationDigest(changed.policy) == baseDigest || reflect.DeepEqual(changed.policy, baseline.policy) {
				t.Fatal("delegation configuration drift retained summary policy digest")
			}
		})
	}
}

func TestDelegationCompactionPolicyMalformedSnapshotsFailClosed(t *testing.T) {
	settings := config.Defaults()
	settings.Workers.DelegateModel = "worker"
	valid, err := sealDelegationCompactionPolicy(settings)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*delegationCompactionPolicy){
		"version": func(p *delegationCompactionPolicy) { p.Version++ },
		"parent digest": func(p *delegationCompactionPolicy) {
			p.Parent.Digest = strings.Repeat("0", 64)
		},
		"child digest": func(p *delegationCompactionPolicy) {
			p.Child.Digest = strings.Repeat("0", 64)
		},
		"child missing point": func(p *delegationCompactionPolicy) {
			p.Child = mustPolicySnapshot(t, &tools.Policy{Default: tools.Deny}, nil)
		},
		"parent missing point": func(p *delegationCompactionPolicy) {
			p.Parent = mustPolicySnapshot(t, &tools.Policy{Default: tools.Allow}, p.Parent.Entries[:2])
		},
		"child widened": func(p *delegationCompactionPolicy) {
			points := policyIdentities(p.Child)
			p.Child = mustPolicySnapshot(t, &tools.Policy{Default: tools.Allow}, points)
		},
		"read flag mismatch":  func(p *delegationCompactionPolicy) { p.ReadTools = !p.ReadTools },
		"read without worker": func(p *delegationCompactionPolicy) { p.Enabled, p.ReadTools = false, true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Parent.Entries = append([]tools.PolicyPoint(nil), valid.Parent.Entries...)
			candidate.Child.Entries = append([]tools.PolicyPoint(nil), valid.Child.Entries...)
			mutate(&candidate)
			if candidate.validate() == nil {
				t.Fatal("malformed delegation policy validated", candidate)
			}
			if body, err := candidate.canonicalJSON(); !errors.Is(err, ErrAdmission) || body != nil {
				t.Fatal("malformed delegation policy serialized", string(body), err)
			}
		})
	}
	settings.Workers.DelegateModel = ""
	settings.Workers.DelegateReadTools = true
	if _, err := sealDelegationCompactionPolicy(settings); !errors.Is(err, ErrAdmission) {
		t.Fatal("read authority without an enabled worker policy sealed", err)
	}

	policyEnvelope := summaryPreparationPolicySnapshot{Version: prepareSummaryVersion, Delegation: valid}
	body, err := json.Marshal(policyEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var canonicalEnvelope map[string]any
	if json.Unmarshal(body, &canonicalEnvelope) != nil {
		t.Fatal("policy envelope did not decode")
	}
	body, err = json.Marshal(canonicalEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := delegationCompactionPolicyFromSnapshot(body); err != nil || !reflect.DeepEqual(parsed, valid) {
		t.Fatal("canonical frozen policy did not round trip", parsed, err)
	}
	for name, raw := range map[string]json.RawMessage{
		"whitespace": append(json.RawMessage(" "), body...),
		"unknown":    json.RawMessage(strings.TrimSuffix(string(body), "}") + `,"unknown":true}`),
		"trailing":   append(append(json.RawMessage(nil), body...), []byte(` {}`)...),
		"truncated":  body[:len(body)-1],
	} {
		t.Run("envelope "+name, func(t *testing.T) {
			if _, err := delegationCompactionPolicyFromSnapshot(raw); !errors.Is(err, ErrAdmission) {
				t.Fatal("malformed frozen policy envelope accepted", string(raw), err)
			}
		})
	}
}

func policyPoints(snapshot tools.PolicySnapshot) map[string]tools.Decision {
	out := make(map[string]tools.Decision, len(snapshot.Entries))
	for _, point := range snapshot.Entries {
		out[point.Tool+"/"+point.Scope] = point.Decision
	}
	return out
}

func policyIdentities(snapshot tools.PolicySnapshot) []tools.PolicyPoint {
	out := make([]tools.PolicyPoint, len(snapshot.Entries))
	for index, point := range snapshot.Entries {
		out[index] = tools.PolicyPoint{Tool: point.Tool, Scope: point.Scope}
	}
	return out
}

func mustPolicySnapshot(t *testing.T, policy *tools.Policy, points []tools.PolicyPoint) tools.PolicySnapshot {
	t.Helper()
	if len(points) > 0 && points[0].Decision != "" {
		points = policyIdentities(tools.PolicySnapshot{Entries: points})
	}
	snapshot, err := tools.SealPolicySnapshot(policy, points)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
