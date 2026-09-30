package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func delegationCompactionBindingFixture(t *testing.T, index int) DelegationCompactionBinding {
	t.Helper()
	batch := index
	binding, err := SealDelegationCompactionBinding(DelegationCompactionBinding{
		ParentTaskID: "parent-task", WorkTaskID: fmt.Sprintf("work-%d", index), ExecutionTaskID: fmt.Sprintf("execution-%d", index),
		WorkerID: fmt.Sprintf("worker-%d", index), Scope: "delegation-parent-task",
		Origin:           runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate_batch", BatchIndex: &batch},
		WorkStartEventID: fmt.Sprintf("work-start-%d", index), WorkStartEventDigest: lifecycleTestDigest(fmt.Sprintf("work-start-%d", index)),
		WorkTerminalEventID: fmt.Sprintf("work-terminal-%d", index), WorkTerminalEventDigest: lifecycleTestDigest(fmt.Sprintf("work-terminal-%d", index)),
		ExecutionStartEventID: fmt.Sprintf("execution-start-%d", index), ExecutionStartEventDigest: lifecycleTestDigest(fmt.Sprintf("execution-start-%d", index)),
		ExecutionContextDigest:   lifecycleTestDigest(fmt.Sprintf("execution-context-%d", index)),
		ExecutionTerminalEventID: fmt.Sprintf("execution-terminal-%d", index), ExecutionTerminalEventDigest: lifecycleTestDigest(fmt.Sprintf("execution-terminal-%d", index)),
		PlanDigest: lifecycleTestDigest("plan"), AuthorityDigest: lifecycleTestDigest("authority"),
		EngineDigest: lifecycleTestDigest("engine"), ParentPolicyDigest: lifecycleTestDigest("parent-policy"),
		ChildPolicyDigest: lifecycleTestDigest("child-policy"), ResultDigest: lifecycleTestDigest(fmt.Sprintf("result-%d", index)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func delegationCompactionActivationFixture(t *testing.T, bindings ...DelegationCompactionBinding) ContextCompactionActivation {
	t.Helper()
	activation, err := SealContextCompactionActivation(ContextCompactionActivation{
		OperationID: "operation", PlanDigest: lifecycleTestDigest("plan"), TaskID: "parent-task", EventID: "activation-event",
		EventSequence: 20, LiveSuffixBoundary: 4, LiveSuffixCount: 5, LiveSuffixDigest: lifecycleTestDigest("suffix"),
		Delegations: bindings, ActivatedAt: time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

func TestDelegationCompactionBindingSealedOwnedAndDigestBound(t *testing.T) {
	index := 0
	input := DelegationCompactionBinding{
		ParentTaskID: "parent-task", WorkTaskID: "work", ExecutionTaskID: "execution", WorkerID: "worker", Scope: "delegation-parent-task",
		Origin:           runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate_batch", BatchIndex: &index},
		WorkStartEventID: "work-start", WorkStartEventDigest: lifecycleTestDigest("work-start"),
		WorkTerminalEventID: "work-terminal", WorkTerminalEventDigest: lifecycleTestDigest("work-terminal"),
		ExecutionStartEventID: "execution-start", ExecutionStartEventDigest: lifecycleTestDigest("execution-start"),
		ExecutionContextDigest:   lifecycleTestDigest("execution-context"),
		ExecutionTerminalEventID: "execution-terminal", ExecutionTerminalEventDigest: lifecycleTestDigest("execution-terminal"),
		PlanDigest: lifecycleTestDigest("plan"), AuthorityDigest: lifecycleTestDigest("authority"),
		EngineDigest: lifecycleTestDigest("engine"), ParentPolicyDigest: lifecycleTestDigest("parent-policy"),
		ChildPolicyDigest: lifecycleTestDigest("child-policy"), ResultDigest: lifecycleTestDigest("result"),
	}
	sealed, err := SealDelegationCompactionBinding(input)
	if err != nil || sealed.Validate() != nil || sealed.Version != ContextCompactionLifecycleVersion || sealed.BindingDigest == "" {
		t.Fatalf("valid binding rejected: %+v %v", sealed, err)
	}
	index = 3
	if sealed.Origin.BatchIndex == nil || *sealed.Origin.BatchIndex != 0 {
		t.Fatal("sealed binding borrowed caller batch index")
	}
	mutated := sealed
	mutated.ResultDigest = lifecycleTestDigest("changed")
	if mutated.Validate() == nil {
		t.Fatal("mutated result retained binding authority")
	}
	mutated = sealed
	mutated.ExecutionContextDigest = lifecycleTestDigest("changed-context")
	if mutated.Validate() == nil {
		t.Fatal("mutated child context retained binding authority")
	}
	mutated = sealed
	mutated.Version++
	if mutated.Validate() == nil {
		t.Fatal("mutated version retained binding authority")
	}
	mutated = sealed
	mutated.BindingDigest = strings.ToUpper(mutated.BindingDigest)
	if mutated.Validate() == nil {
		t.Fatal("noncanonical binding digest retained authority")
	}
	mutated = sealed
	*mutated.Origin.BatchIndex = 1
	if mutated.Validate() == nil {
		t.Fatal("mutated origin retained binding authority")
	}
	single := sealed
	single.Origin.ToolName, single.Origin.BatchIndex, single.BindingDigest = "delegate", nil, ""
	if single, err = SealDelegationCompactionBinding(single); err != nil || single.Validate() != nil {
		t.Fatal("single delegation semantics rejected", err)
	}
}

func TestDelegationCompactionBindingRejectsMalformedEvidence(t *testing.T) {
	valid := delegationCompactionBindingFixture(t, 0)
	tests := map[string]func(*DelegationCompactionBinding){
		"parent":                    func(b *DelegationCompactionBinding) { b.ParentTaskID = "" },
		"work":                      func(b *DelegationCompactionBinding) { b.WorkTaskID = "bad\nwork" },
		"execution":                 func(b *DelegationCompactionBinding) { b.ExecutionTaskID = "" },
		"worker":                    func(b *DelegationCompactionBinding) { b.WorkerID = "" },
		"parent work alias":         func(b *DelegationCompactionBinding) { b.WorkTaskID = b.ParentTaskID },
		"parent execution alias":    func(b *DelegationCompactionBinding) { b.ExecutionTaskID = b.ParentTaskID },
		"work execution alias":      func(b *DelegationCompactionBinding) { b.ExecutionTaskID = b.WorkTaskID },
		"scope":                     func(b *DelegationCompactionBinding) { b.Scope = "delegation-other" },
		"scope control":             func(b *DelegationCompactionBinding) { b.ParentTaskID += "\n"; b.Scope = "delegation-" + b.ParentTaskID },
		"origin tool":               func(b *DelegationCompactionBinding) { b.Origin.ToolName = "shell" },
		"origin batch":              func(b *DelegationCompactionBinding) { n := 4; b.Origin.BatchIndex = &n },
		"work start id":             func(b *DelegationCompactionBinding) { b.WorkStartEventID = "" },
		"work start digest":         func(b *DelegationCompactionBinding) { b.WorkStartEventDigest = strings.Repeat("g", 64) },
		"work terminal id":          func(b *DelegationCompactionBinding) { b.WorkTerminalEventID = "" },
		"work terminal digest":      func(b *DelegationCompactionBinding) { b.WorkTerminalEventDigest = "bad" },
		"execution start id":        func(b *DelegationCompactionBinding) { b.ExecutionStartEventID = "" },
		"execution start digest":    func(b *DelegationCompactionBinding) { b.ExecutionStartEventDigest = "bad" },
		"execution context digest":  func(b *DelegationCompactionBinding) { b.ExecutionContextDigest = "bad" },
		"execution terminal id":     func(b *DelegationCompactionBinding) { b.ExecutionTerminalEventID = "" },
		"execution terminal digest": func(b *DelegationCompactionBinding) { b.ExecutionTerminalEventDigest = "bad" },
		"duplicate event ids":       func(b *DelegationCompactionBinding) { b.ExecutionStartEventID = b.WorkStartEventID },
		"engine digest":             func(b *DelegationCompactionBinding) { b.EngineDigest = "bad" },
		"parent policy digest":      func(b *DelegationCompactionBinding) { b.ParentPolicyDigest = "bad" },
		"child policy digest":       func(b *DelegationCompactionBinding) { b.ChildPolicyDigest = "bad" },
		"result digest":             func(b *DelegationCompactionBinding) { b.ResultDigest = "bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			if valid.Origin.BatchIndex != nil {
				index := *valid.Origin.BatchIndex
				candidate.Origin.BatchIndex = &index
			}
			mutate(&candidate)
			candidate.BindingDigest = ""
			if _, err := SealDelegationCompactionBinding(candidate); !errors.Is(err, ErrContextCompactionLifecycle) {
				t.Fatalf("malformed binding accepted: %+v %v", candidate, err)
			}
		})
	}
}

func TestContextCompactionActivationDelegationsAreOrderedUniqueAndOwned(t *testing.T) {
	first, second := delegationCompactionBindingFixture(t, 0), delegationCompactionBindingFixture(t, 1)
	input := []DelegationCompactionBinding{first, second}
	activation := delegationCompactionActivationFixture(t, input...)
	if activation.Validate() != nil || activation.Delegations == nil || len(activation.Delegations) != 2 {
		t.Fatalf("valid delegated activation rejected: %+v", activation)
	}
	originalDigest := activation.ActivationDigest
	input[0].WorkTaskID = "caller-edit"
	*first.Origin.BatchIndex = 3
	if activation.Delegations[0].WorkTaskID != "work-0" || *activation.Delegations[0].Origin.BatchIndex != 0 || activation.Validate() != nil {
		t.Fatal("activation borrowed delegation evidence")
	}
	changed := activation
	changed.Delegations = append([]DelegationCompactionBinding(nil), activation.Delegations...)
	changed.Delegations[0].ResultDigest = lifecycleTestDigest("changed-result")
	if changed.Validate() == nil || changed.ActivationDigest != originalDigest {
		t.Fatal("changed delegation evidence retained activation authority")
	}

	empty := delegationCompactionActivationFixture(t)
	if empty.Delegations == nil || len(empty.Delegations) != 0 || empty.Validate() != nil {
		t.Fatal("sealed nondelegated activation did not own an empty slice")
	}
	body, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var legacy ContextCompactionActivation
	if json.Unmarshal(body, &legacy) != nil || legacy.Validate() != nil {
		t.Fatal("empty/omitted delegation evidence lost legacy compatibility")
	}
}

func TestContextCompactionActivationRejectsDelegationSetConflicts(t *testing.T) {
	first, second := delegationCompactionBindingFixture(t, 0), delegationCompactionBindingFixture(t, 1)
	tests := map[string][]DelegationCompactionBinding{
		"unsorted":          {second, first},
		"duplicate binding": {first, first},
		"wrong parent": {func() DelegationCompactionBinding {
			b := first
			b.ParentTaskID, b.Scope = "other", "delegation-other"
			return resealDelegationBinding(t, b)
		}()},
		"duplicate work": {first, func() DelegationCompactionBinding {
			b := second
			b.WorkTaskID = first.WorkTaskID
			return resealDelegationBinding(t, b)
		}()},
		"duplicate execution": {first, func() DelegationCompactionBinding {
			b := second
			b.ExecutionTaskID = first.ExecutionTaskID
			return resealDelegationBinding(t, b)
		}()},
		"cross task alias": {first, func() DelegationCompactionBinding {
			b := second
			b.WorkTaskID = first.ExecutionTaskID
			return resealDelegationBinding(t, b)
		}()},
		"duplicate origin": {first, func() DelegationCompactionBinding {
			b := second
			index := *first.Origin.BatchIndex
			b.Origin = first.Origin
			b.Origin.BatchIndex = &index
			return resealDelegationBinding(t, b)
		}()},
		"duplicate event": {first, func() DelegationCompactionBinding {
			b := second
			b.WorkStartEventID = first.WorkStartEventID
			return resealDelegationBinding(t, b)
		}()},
	}
	for name, bindings := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := SealContextCompactionActivation(ContextCompactionActivation{
				OperationID: "operation", PlanDigest: lifecycleTestDigest("plan"), TaskID: "parent-task", EventID: "activation-event",
				EventSequence: 20, LiveSuffixBoundary: 4, LiveSuffixCount: 5, LiveSuffixDigest: lifecycleTestDigest("suffix"),
				Delegations: bindings, ActivatedAt: time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC),
			})
			if !errors.Is(err, ErrContextCompactionLifecycle) {
				t.Fatalf("conflicting delegation set accepted: %v", err)
			}
		})
	}

	tooMany := make([]DelegationCompactionBinding, MaxDelegationCompactionBindings+1)
	for i := range tooMany {
		tooMany[i] = first
		// The protocol only permits four positions per batch call. Give each
		// binding a distinct call identity while retaining canonical order.
		tooMany[i].WorkTaskID = fmt.Sprintf("many-work-%03d", i)
		tooMany[i].ExecutionTaskID = fmt.Sprintf("many-execution-%03d", i)
		tooMany[i].WorkerID = fmt.Sprintf("many-worker-%03d", i)
		tooMany[i].Origin.ToolCallID = fmt.Sprintf("call-%03d", i)
		tooMany[i].Origin.BatchIndex = func() *int { n := 0; return &n }()
		tooMany[i].WorkStartEventID = fmt.Sprintf("many-work-start-%03d", i)
		tooMany[i].WorkStartEventDigest = lifecycleTestDigest(tooMany[i].WorkStartEventID)
		tooMany[i].WorkTerminalEventID = fmt.Sprintf("many-work-terminal-%03d", i)
		tooMany[i].WorkTerminalEventDigest = lifecycleTestDigest(tooMany[i].WorkTerminalEventID)
		tooMany[i].ExecutionStartEventID = fmt.Sprintf("many-execution-start-%03d", i)
		tooMany[i].ExecutionStartEventDigest = lifecycleTestDigest(tooMany[i].ExecutionStartEventID)
		tooMany[i].ExecutionContextDigest = lifecycleTestDigest(fmt.Sprintf("many-execution-context-%03d", i))
		tooMany[i].ExecutionTerminalEventID = fmt.Sprintf("many-execution-terminal-%03d", i)
		tooMany[i].ExecutionTerminalEventDigest = lifecycleTestDigest(tooMany[i].ExecutionTerminalEventID)
		tooMany[i].ResultDigest = lifecycleTestDigest(fmt.Sprintf("many-result-%03d", i))
		tooMany[i] = resealDelegationBinding(t, tooMany[i])
	}
	if _, err := SealContextCompactionActivation(ContextCompactionActivation{
		OperationID: "operation", PlanDigest: lifecycleTestDigest("plan"), TaskID: "parent-task", EventID: "activation-event",
		EventSequence: 20, LiveSuffixBoundary: 4, LiveSuffixCount: 5, LiveSuffixDigest: lifecycleTestDigest("suffix"),
		Delegations: tooMany, ActivatedAt: time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC),
	}); !errors.Is(err, ErrContextCompactionLifecycle) {
		t.Fatalf("oversized delegation set accepted: %v", err)
	}
}

func resealDelegationBinding(t *testing.T, binding DelegationCompactionBinding) DelegationCompactionBinding {
	t.Helper()
	binding.BindingDigest = ""
	sealed, err := SealDelegationCompactionBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestContextCompactionFactOwnsDelegationBindings(t *testing.T) {
	binding := delegationCompactionBindingFixture(t, 0)
	activation := delegationCompactionActivationFixture(t, binding)
	fact, err := SealContextCompactionLifecycleFact(ContextCompactionLifecycleFact{
		ID: "activated-fact", OperationID: "operation", Sequence: 5, PreviousID: "approved-fact", Kind: ContextCompactionActivated,
		PlanDigest: activation.PlanDigest, SummaryAttemptID: "attempt", SummaryReviewID: "review", Activation: &activation,
		CreatedAt: activation.ActivatedAt,
	})
	if err != nil || fact.Validate() != nil {
		t.Fatal(err)
	}
	activation.Delegations[0].WorkTaskID = "caller-edit"
	if fact.Activation.Delegations[0].WorkTaskID != "work-0" || fact.Validate() != nil {
		t.Fatal("lifecycle fact borrowed activation delegation slice")
	}
	if reflect.DeepEqual(fact.Activation.Delegations, activation.Delegations) {
		t.Fatal("fact delegation ownership test did not mutate its input")
	}
}
