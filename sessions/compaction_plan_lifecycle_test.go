package sessions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func lifecycleTestDigest(seed string) string {
	digest, err := lifecycleDigest(seed)
	if err != nil {
		panic(err)
	}
	return digest
}

func lifecycleTestStart(t *testing.T) ContextCompactionPlanStart {
	t.Helper()
	engine, err := runtime.NewContextEngineIdentity("test.engine", "v1")
	if err != nil {
		t.Fatal(err)
	}
	tiers, err := runtime.NewContextTierPlan(lifecycleTestDigest("stable"), lifecycleTestDigest("project"), lifecycleTestDigest("volatile"), []runtime.ContextTier{runtime.ContextTierStable, runtime.ContextTierProject, runtime.ContextTierVolatile})
	if err != nil {
		t.Fatal(err)
	}
	start, err := SealContextCompactionPlanStart(ContextCompactionPlanStart{
		OperationID: "operation-1", RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1",
		SourceSequence: 12, SourceDigest: lifecycleTestDigest("source"), Model: "model-1", Provider: "provider-1",
		Keep: 4, EstimatedCost: 0.25, ConfigSnapshot: json.RawMessage(`{"weight":2,"mode":"hybrid"}`),
		PolicySnapshot: json.RawMessage(`{"egress":"deny","approvals":true}`), Engine: engine, Tiers: tiers,
		ProcessID: "process-1", StartedAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return start
}

func lifecycleTestFact(t *testing.T, kind ContextCompactionLifecycleKind, sequence int, previous *ContextCompactionLifecycleFact, plan bool) ContextCompactionLifecycleFact {
	t.Helper()
	fact := ContextCompactionLifecycleFact{
		ID: "fact-" + string(rune('0'+sequence)), OperationID: "operation-1", Sequence: sequence, Kind: kind,
		CreatedAt: time.Date(2026, 9, 15, 12, sequence, 0, 0, time.UTC),
	}
	if previous != nil {
		fact.PreviousID = previous.ID
	}
	if plan {
		fact.PlanDigest, fact.SummaryAttemptID, fact.SummaryReviewID = lifecycleTestDigest("plan"), "attempt-1", "review-1"
	}
	if kind == ContextCompactionRevoked || kind == ContextCompactionFailed {
		fact.Code = "owner_interrupted"
	}
	sealed, err := SealContextCompactionLifecycleFact(fact)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestContextCompactionPlanStartCanonicalAndOwned(t *testing.T) {
	start := lifecycleTestStart(t)
	if start.Validate() != nil || string(start.ConfigSnapshot) != `{"mode":"hybrid","weight":2}` {
		t.Fatalf("invalid canonical start: %+v", start)
	}
	requestDigest := start.RequestDigest
	changedOwner := start
	changedOwner.OperationID, changedOwner.ProcessID = "operation-2", "process-2"
	changedOwner.StartedAt = changedOwner.StartedAt.Add(time.Hour)
	if digest, err := changedOwner.CanonicalRequestDigest(); err != nil || digest != requestDigest {
		t.Fatalf("request digest changed with operation ownership: %q %v", digest, err)
	}
	mutated := start
	mutated.Keep++
	if mutated.Validate() == nil {
		t.Fatal("mutated request retained authority")
	}
	input := ContextCompactionPlanStart{
		OperationID: "operation-1", RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1",
		SourceSequence: 1, SourceDigest: lifecycleTestDigest("source"), Model: "model", Provider: "provider", Keep: 1,
		ConfigSnapshot: json.RawMessage(`{"x":1,"x":2}`), PolicySnapshot: json.RawMessage(`{}`), Engine: start.Engine,
		Tiers: start.Tiers, ProcessID: "process", StartedAt: time.Now().UTC(),
	}
	if _, err := SealContextCompactionPlanStart(input); !errors.Is(err, ErrContextCompactionLifecycle) {
		t.Fatalf("duplicate JSON key accepted: %v", err)
	}
	input.ConfigSnapshot = json.RawMessage(`{"x":1} trailing`)
	if _, err := SealContextCompactionPlanStart(input); !errors.Is(err, ErrContextCompactionLifecycle) {
		t.Fatalf("trailing JSON accepted: %v", err)
	}
	input.ConfigSnapshot = json.RawMessage(`{"x":"` + strings.Repeat("x", maxContextCompactionSnapshotBytes) + `"}`)
	if _, err := SealContextCompactionPlanStart(input); !errors.Is(err, ErrContextCompactionLifecycle) {
		t.Fatalf("oversize snapshot accepted: %v", err)
	}
}

func TestContextCompactionLifecycleTransitionsAndTerminalStates(t *testing.T) {
	started := lifecycleTestFact(t, ContextCompactionStarted, 1, nil, false)
	prepared := lifecycleTestFact(t, ContextCompactionPrepared, 2, &started, true)
	validated := lifecycleTestFact(t, ContextCompactionValidated, 3, &prepared, true)
	approved := lifecycleTestFact(t, ContextCompactionApproved, 4, &validated, true)
	for _, pair := range []struct {
		previous *ContextCompactionLifecycleFact
		next     ContextCompactionLifecycleFact
	}{{nil, started}, {&started, prepared}, {&prepared, validated}, {&validated, approved}} {
		if err := ValidateContextCompactionTransition(pair.previous, pair.next); err != nil {
			t.Fatalf("valid transition rejected: %v", err)
		}
	}
	failed := lifecycleTestFact(t, ContextCompactionFailed, 2, &started, false)
	if ValidateContextCompactionTransition(&started, failed) != nil {
		t.Fatal("started -> failed rejected")
	}
	if ValidateContextCompactionTransition(&approved, failed) == nil {
		t.Fatal("mismatched plan evidence accepted")
	}
	terminalNext := lifecycleTestFact(t, ContextCompactionFailed, 3, &failed, false)
	if ValidateContextCompactionTransition(&failed, terminalNext) == nil {
		t.Fatal("transition after terminal fact accepted")
	}
	tampered := prepared
	tampered.SummaryReviewID = "review-2"
	if tampered.Validate() == nil {
		t.Fatal("tampered fact retained authority")
	}
}

func TestContextCompactionActivationBindsExactSuffix(t *testing.T) {
	empty, err := ContextCompactionLiveSuffixDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	emptySlice, err := ContextCompactionLiveSuffixDigest([]providers.Message{})
	if err != nil || empty != emptySlice {
		t.Fatalf("empty suffix was not canonical: %q %q %v", empty, emptySlice, err)
	}
	suffix := []providers.Message{{Role: "user", Content: "new work"}}
	digest, err := ContextCompactionLiveSuffixDigest(suffix)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := SealContextCompactionActivation(ContextCompactionActivation{
		OperationID: "operation-1", PlanDigest: lifecycleTestDigest("plan"), TaskID: "task-1", EventID: "event-20",
		EventSequence: 20, LiveSuffixBoundary: 8, LiveSuffixCount: len(suffix), LiveSuffixDigest: digest,
		ActivatedAt: time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC),
	})
	if err != nil || activation.Validate() != nil {
		t.Fatalf("valid activation rejected: %+v %v", activation, err)
	}
	activation.LiveSuffixCount++
	if activation.Validate() == nil {
		t.Fatal("mutated suffix count retained authority")
	}
}

func TestContextCompactionOperationStateAndRecovery(t *testing.T) {
	start := lifecycleTestStart(t)
	started := lifecycleTestFact(t, ContextCompactionStarted, 1, nil, false)
	failed := lifecycleTestFact(t, ContextCompactionFailed, 2, &started, false)
	recovery, err := SealContextCompactionRecovery(ContextCompactionRecovery{
		ID: "recovery-1", OperationID: start.OperationID, ProcessID: "process-2", FailedFactID: failed.ID,
		FailedFactDigest: failed.Digest, RecoveredAt: failed.CreatedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := SealContextCompactionOperationState(ContextCompactionOperationState{
		Start: start, Status: ContextCompactionFailed, Facts: []ContextCompactionLifecycleFact{started, failed}, Recovery: &recovery,
	})
	if err != nil || state.Validate() != nil {
		t.Fatalf("valid recovered state rejected: %v", err)
	}
	state.Facts[0].ID = "aliased-edit"
	if state.Validate() == nil {
		t.Fatal("mutated returned state retained authority")
	}
	badRecovery := recovery
	badRecovery.ProcessID = start.ProcessID
	badRecovery, err = SealContextCompactionRecovery(badRecovery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SealContextCompactionOperationState(ContextCompactionOperationState{
		Start: start, Status: ContextCompactionFailed, Facts: []ContextCompactionLifecycleFact{started, failed}, Recovery: &badRecovery,
	}); !errors.Is(err, ErrContextCompactionLifecycle) {
		t.Fatalf("original owner recovered itself: %v", err)
	}
}
