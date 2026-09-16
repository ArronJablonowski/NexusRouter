package tools

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSealPolicySnapshotCanonicalizesAndOwnsTargets(t *testing.T) {
	policy := &Policy{Default: Deny, Rules: []Rule{
		{Tool: "read_file", Scope: "workspace", Decision: Allow},
		{Tool: "inspect", Scope: "project", Decision: Ask},
	}}
	targets := []PolicyPoint{
		{Tool: "read_file", Scope: "workspace"},
		{Tool: "inspect", Scope: "project"},
		{Tool: "read_file", Scope: "workspace"},
		{Tool: "absent", Scope: "resource"},
	}
	sealed, err := SealPolicySnapshot(policy, targets)
	if err != nil || sealed.Validate() != nil {
		t.Fatal(sealed, err)
	}
	targets[0] = PolicyPoint{Tool: "changed", Scope: "changed"}
	want := []PolicyPoint{
		{Tool: "absent", Scope: "resource", Decision: Deny},
		{Tool: "inspect", Scope: "project", Decision: Ask},
		{Tool: "read_file", Scope: "workspace", Decision: Allow},
	}
	if sealed.Version != PolicySnapshotVersion || !reflect.DeepEqual(sealed.Entries, want) || len(sealed.Digest) != 64 {
		t.Fatal("snapshot was not canonical and owned", sealed)
	}
	reordered, err := SealPolicySnapshot(policy, []PolicyPoint{targets[3], targets[1], {Tool: "read_file", Scope: "workspace"}})
	if err != nil || !reflect.DeepEqual(reordered, sealed) {
		t.Fatal("input order changed canonical snapshot", reordered, err)
	}
	empty, err := SealPolicySnapshot(nil, nil)
	if err != nil || empty.Validate() != nil || empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatal("empty deny snapshot is not canonical", empty, err)
	}
}

func TestSealPolicySnapshotResolvesValidatedParentChains(t *testing.T) {
	grandparent := &Policy{Default: Deny, Rules: []Rule{
		{Tool: "*", Scope: "workspace", Decision: Allow},
		{Tool: "blocked", Scope: "workspace", Decision: Deny},
	}}
	parent := &Policy{Default: Allow, Parent: grandparent, Rules: []Rule{{Tool: "review", Scope: "workspace", Decision: Ask}}}
	child := &Policy{Default: Allow, Parent: parent}
	snapshot, err := SealPolicySnapshot(child, []PolicyPoint{
		{Tool: "ordinary", Scope: "workspace"},
		{Tool: "review", Scope: "workspace"},
		{Tool: "blocked", Scope: "workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Decision{}
	for _, entry := range snapshot.Entries {
		got[entry.Tool] = entry.Decision
	}
	if got["ordinary"] != Allow || got["review"] != Ask || got["blocked"] != Deny {
		t.Fatal("parent-chain decisions were not resolved", got)
	}
	grandparent.Rules[0].Decision = Deny
	if got["ordinary"] != Allow || snapshot.Validate() != nil {
		t.Fatal("sealed evidence retained caller policy aliases", snapshot)
	}
	cycle := &Policy{Default: Allow}
	cycle.Parent = cycle
	if _, err := SealPolicySnapshot(cycle, []PolicyPoint{{Tool: "read_file", Scope: "workspace"}}); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatal("cyclic policy accepted", err)
	}
}

func TestSealPolicySnapshotRejectsInvalidExactTargets(t *testing.T) {
	invalid := []PolicyPoint{
		{Tool: "*", Scope: "workspace"},
		{Tool: "read-file", Scope: "workspace"},
		{Tool: "read_file", Scope: "*"},
		{Tool: "read_file", Scope: " workspace"},
		{Tool: "read_file", Scope: "workspace\nsecret"},
		{Tool: "read_file\x00", Scope: "workspace"},
		{Tool: "", Scope: "workspace"},
		{Tool: "read_file", Scope: ""},
	}
	for _, target := range invalid {
		if _, err := SealPolicySnapshot(&Policy{Default: Allow}, []PolicyPoint{target}); !errors.Is(err, ErrPolicySnapshot) {
			t.Fatalf("invalid exact target accepted: %#v: %v", target, err)
		}
	}
	many := make([]PolicyPoint, maxPolicySnapshotEntries+1)
	for i := range many {
		many[i] = PolicyPoint{Tool: "read_file", Scope: "workspace"}
	}
	if _, err := SealPolicySnapshot(nil, many); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatal("oversized target input accepted", err)
	}
	large := make([]PolicyPoint, 300)
	for i := range large {
		large[i] = PolicyPoint{Tool: "read_file", Scope: strings.Repeat("s", 250) + string(rune('A'+i%26)) + string(rune('A'+i/26))}
	}
	if _, err := SealPolicySnapshot(nil, large); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatal("oversized canonical snapshot accepted", err)
	}
	badPolicy := &Policy{Default: Decision("permit")}
	if _, err := SealPolicySnapshot(badPolicy, []PolicyPoint{{Tool: "read_file", Scope: "workspace"}}); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatal("invalid policy accepted", err)
	}
	if _, err := SealPolicySnapshot(nil, []PolicyPoint{{Tool: "read_file", Scope: "workspace", Decision: Allow}}); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatal("caller-supplied decision accepted", err)
	}
}

func TestPolicySnapshotValidationRejectsMutation(t *testing.T) {
	base, err := SealPolicySnapshot(&Policy{Default: Allow}, []PolicyPoint{
		{Tool: "a", Scope: "scope"}, {Tool: "b", Scope: "scope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*PolicySnapshot){
		"version":   func(s *PolicySnapshot) { s.Version++ },
		"nil":       func(s *PolicySnapshot) { s.Entries = nil },
		"tool":      func(s *PolicySnapshot) { s.Entries[0].Tool = "changed" },
		"scope":     func(s *PolicySnapshot) { s.Entries[0].Scope = "changed" },
		"decision":  func(s *PolicySnapshot) { s.Entries[0].Decision = Deny },
		"invalid":   func(s *PolicySnapshot) { s.Entries[0].Decision = Decision("invalid") },
		"wildcard":  func(s *PolicySnapshot) { s.Entries[0].Scope = "*" },
		"duplicate": func(s *PolicySnapshot) { s.Entries[1] = s.Entries[0] },
		"order":     func(s *PolicySnapshot) { s.Entries[0], s.Entries[1] = s.Entries[1], s.Entries[0] },
		"digest":    func(s *PolicySnapshot) { s.Digest = strings.Repeat("0", 64) },
		"encoding":  func(s *PolicySnapshot) { s.Digest = strings.Repeat("z", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := base
			copy.Entries = append([]PolicyPoint(nil), base.Entries...)
			mutate(&copy)
			if copy.Validate() == nil {
				t.Fatal("mutated snapshot validated", copy)
			}
		})
	}
	if base.Validate() != nil {
		t.Fatal("mutation changed original snapshot")
	}
	left, err := base.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	right, err := base.CanonicalJSON()
	if err != nil || string(left) != string(right) || len(left) == 0 {
		t.Fatal("canonical JSON is unstable", string(left), string(right), err)
	}
	corrupt := base
	corrupt.Digest = strings.Repeat("0", 64)
	if body, err := corrupt.CanonicalJSON(); !errors.Is(err, ErrPolicySnapshot) || body != nil {
		t.Fatal("canonical JSON accepted invalid evidence", string(body), err)
	}
}

func TestPolicySnapshotEqualOrStricterDecisionOrderAndSubset(t *testing.T) {
	targetA := PolicyPoint{Tool: "a", Scope: "scope"}
	targetB := PolicyPoint{Tool: "b", Scope: "scope"}
	seal := func(decision Decision, targets ...PolicyPoint) PolicySnapshot {
		t.Helper()
		snapshot, err := SealPolicySnapshot(&Policy{Default: decision}, targets)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	decisions := []Decision{Deny, Ask, Allow}
	for parentRank, parentDecision := range decisions {
		for childRank, childDecision := range decisions {
			parent := seal(parentDecision, targetA)
			child := seal(childDecision, targetA)
			want := childRank <= parentRank
			if got := child.AtMost(parent); got != want {
				t.Fatalf("parent=%s child=%s got=%v want=%v", parentDecision, childDecision, got, want)
			}
		}
	}
	parent := seal(Allow, targetA, targetB)
	if !seal(Ask, targetA).AtMost(parent) || !seal(Deny).AtMost(parent) {
		t.Fatal("subset or empty child was not accepted as stricter")
	}
	if seal(Deny, targetA, targetB).AtMost(seal(Allow, targetA)) {
		t.Fatal("child target absent from parent was accepted")
	}
	corrupt := seal(Deny, targetA)
	corrupt.Digest = strings.Repeat("0", 64)
	if corrupt.AtMost(parent) || seal(Deny).AtMost(corrupt) {
		t.Fatal("invalid snapshot participated in policy proof")
	}
}
