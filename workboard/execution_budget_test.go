package workboard

import (
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func executionEvent() runtime.Event {
	return runtime.Event{Version: 1, ID: "event", TaskID: "task", SessionID: "session", CorrelationID: "task", WorkerID: "worker",
		Sequence: 1, Time: time.Unix(1, 0).UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ModelID: "model", ProviderID: "provider", ConfigID: strings.Repeat("a", 64)}}
}

func executionReservation() ExecutionReservation {
	return ExecutionReservation{Version: 1, ModelID: "model", ProviderID: "provider", ConfigID: strings.Repeat("a", 64),
		TimeLimitMS: 1000, TokenLimit: 2000, CostMicros: 3000, GlobalWIPLimit: 4, BoardWIPLimit: 2}
}

func TestExecutionReservationBindsRuntimeAndCapacity(t *testing.T) {
	event, reservation := executionEvent(), executionReservation()
	if err := reservation.Validate(event); err != nil {
		t.Fatal(err)
	}
	digest, err := reservation.CanonicalDigest()
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	for name, mutate := range map[string]func(*ExecutionReservation){
		"model":  func(r *ExecutionReservation) { r.ModelID = "other" },
		"config": func(r *ExecutionReservation) { r.ConfigID = "secret" },
		"wip":    func(r *ExecutionReservation) { r.BoardWIPLimit = r.GlobalWIPLimit + 1 },
		"tokens": func(r *ExecutionReservation) { r.TokenLimit = MaxWorkTokens + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := reservation
			mutate(&invalid)
			if invalid.Validate(event) == nil {
				t.Fatal("invalid reservation accepted")
			}
		})
	}
}

func TestExecutionRecordsRequireCanonicalDigests(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	admission := ExecutionAdmissionRecord{Version: 1, AdmissionID: "admission", TaskID: "task", SessionID: "session", EventID: "event",
		EventDigest: strings.Repeat("b", 64), BoardID: "board", CardID: "card", AttemptID: "attempt", ClaimID: "claim", WorkerID: "worker",
		OperationID: "operation", RequestDigest: strings.Repeat("c", 64), CardRevision: 2, ModelID: "model", ProviderID: "provider", ConfigID: strings.Repeat("d", 64),
		PolicyDigest: strings.Repeat("e", 64), TimeLimitMS: 100, TokenLimit: 200, CostMicros: 300, GlobalWIPLimit: 2, BoardWIPLimit: 1, AdmittedAt: now}
	admission.AdmissionDigest, _ = admission.CanonicalDigest()
	if err := admission.Validate(); err != nil {
		t.Fatal(err)
	}
	settlement := ExecutionSettlementRecord{Version: 1, SettlementID: "settlement", AdmissionID: admission.AdmissionID, AdmissionDigest: admission.AdmissionDigest,
		TaskID: admission.TaskID, SessionID: admission.SessionID, BoardID: admission.BoardID, CardID: admission.CardID, AttemptID: admission.AttemptID,
		ClaimID: admission.ClaimID, WorkerID: admission.WorkerID, OperationID: admission.OperationID, ModelID: admission.ModelID, ProviderID: admission.ProviderID,
		ConfigID: admission.ConfigID, TerminalEventID: "terminal", TerminalEventDigest: strings.Repeat("f", 64), TerminalKind: runtime.TaskFailed,
		TerminalSequence: 2, ChargedTimeMS: 10, ChargedTokens: 200, ChargedCostMicros: 300, SettledAt: now.Add(time.Second)}
	settlement.SettlementDigest, _ = settlement.CanonicalDigest()
	if err := settlement.Validate(); err != nil {
		t.Fatal(err)
	}
	settlement.ModelID = "forged"
	if settlement.Validate() == nil {
		t.Fatal("digest did not bind settlement")
	}
}
