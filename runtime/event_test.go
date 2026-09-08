package runtime

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestCanonicalEventKindsEncode(t *testing.T) {
	accepted := true
	base := Event{Version: 1, ID: "id", TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: 1, Time: time.Now()}
	tests := []struct {
		kind Kind
		set  func(*Event)
	}{
		{TaskStarted, nil},
		{TaskCompleted, nil},
		{TaskFailed, nil},
		{TaskCanceled, nil},
		{TurnStarted, func(e *Event) { e.TurnID = "turn" }},
		{TurnCompleted, func(e *Event) { e.TurnID = "turn" }},
		{ModelDelta, func(e *Event) { e.TurnID = "turn"; e.Data.Text = "delta" }},
		{ToolStarted, func(e *Event) {
			e.TurnID, e.Data.ToolCallID, e.Data.ToolName, e.Data.Effect = "turn", "call", "lookup", UncertainEffect
		}},
		{ToolCompleted, func(e *Event) {
			e.TurnID, e.Data.ToolCallID, e.Data.ToolName, e.Data.Effect = "turn", "call", "lookup", NoEffect
		}},
		{WorkerStarted, func(e *Event) { e.WorkerID = "worker" }},
		{WorkerHeartbeat, func(e *Event) { e.WorkerID = "worker" }},
		{WorkerCompleted, func(e *Event) { e.WorkerID = "worker" }},
		{RouteSelected, func(e *Event) {
			e.RouteID, e.Data.ModelID, e.Data.ProviderID = "route", "model", "provider"
		}},
		{EvaluationRecorded, func(e *Event) { e.Data.Accepted = &accepted }},
		{ErrorRecorded, func(e *Event) { e.Data.Code = "provider_unavailable" }},
		{SteeringApplied, func(e *Event) { e.Data.SteeringID, e.Data.Text = "steering", "continue with tests" }},
	}

	seen := make(map[Kind]struct{}, len(tests))
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			event := base
			event.Kind = test.kind
			if test.set != nil {
				test.set(&event)
			}
			body, err := event.Encode()
			if err != nil {
				t.Fatal(err)
			}
			var decoded Event
			if err := json.Unmarshal(body, &decoded); err != nil || decoded.Version != 1 || decoded.Kind != test.kind {
				t.Fatalf("event did not round trip: %+v %v", decoded, err)
			}
		})
		if _, duplicate := seen[test.kind]; duplicate {
			t.Fatalf("duplicate canonical event test for %q", test.kind)
		}
		seen[test.kind] = struct{}{}
	}
}

func TestEventValidation(t *testing.T) {
	base := Event{Version: 1, ID: "id", TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: 1, Time: time.Now(), Kind: TaskStarted}
	if _, err := base.Encode(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){
		func(e *Event) { e.Version = 2 },
		func(e *Event) { e.Sequence = 0 },
		func(e *Event) { e.Kind = "unknown" },
		func(e *Event) { e.Kind = ToolCompleted },
		func(e *Event) { e.Kind = EvaluationRecorded },
		func(e *Event) { e.Kind = RouteSelected },
		func(e *Event) { e.Kind = ModelDelta },
		func(e *Event) { v := -1.0; e.Data.RouteEstimatedCost = &v },
		func(e *Event) { v := math.NaN(); e.Data.RouteEstimatedCost = &v },
		func(e *Event) { v := 1.0; e.Data.RouteEstimatedCost = &v; e.Kind = TaskCompleted },
	} {
		e := base
		mutate(&e)
		if _, err := e.Encode(); err == nil {
			t.Fatalf("invalid event accepted: %+v", e)
		}
	}
}

func TestDelegationAuditEventPlacementValidation(t *testing.T) {
	now := time.Now()
	intent := &DelegationAuditIntent{Version: 1, OperationID: "operation", ReviewerID: "reviewer"}
	confidence := .5
	outcome := &DelegationAudit{Version: 1, OperationID: "operation", ReviewerID: "reviewer", AuditID: "audit", Status: "completed", Verdict: "accept", Confidence: &confidence, Citations: []string{"candidate"}}
	started := Event{Version: 1, ID: "started", TaskID: "work", SessionID: "session", CorrelationID: "work", WorkerID: "worker", Sequence: 1, Time: now, Kind: TaskStarted, Data: Data{ParentTaskID: "parent", DelegationAuditIntent: intent}}
	completed := Event{Version: 1, ID: "completed", TaskID: "work", SessionID: "session", CorrelationID: "work", WorkerID: "worker", Sequence: 2, Time: now, Kind: WorkerCompleted, Data: Data{Text: "answer", DelegationAudit: outcome}}
	if started.Validate() != nil || completed.Validate() != nil {
		t.Fatal("valid audit event placement rejected")
	}
	for name, mutate := range map[string]func(*Event){
		"intent wrong kind": func(e *Event) { e.Kind = TaskCompleted },
		"intent no worker":  func(e *Event) { e.WorkerID = "" },
		"intent malformed": func(e *Event) {
			e.Data.DelegationAuditIntent = &DelegationAuditIntent{Version: 1, OperationID: "raw operation", ReviewerID: "reviewer"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := started
			mutate(&e)
			if e.Validate() == nil {
				t.Fatal("invalid intent event accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*Event){
		"outcome wrong kind": func(e *Event) { e.Kind = WorkerStarted },
		"outcome no worker":  func(e *Event) { e.WorkerID = "" },
		"outcome malformed": func(e *Event) {
			copy := *e.Data.DelegationAudit
			copy.ReviewerID = ""
			e.Data.DelegationAudit = &copy
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := completed
			mutate(&e)
			if e.Validate() == nil {
				t.Fatal("invalid outcome event accepted")
			}
		})
	}
	changed := *intent
	changed.ReviewerID = "other"
	if outcome.Validate(&changed) == nil {
		t.Fatal("cross-event reviewer mismatch accepted")
	}
}
