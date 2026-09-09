package runtime

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
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
		{ContextCompacted, func(e *Event) {
			e.Data.ParentTaskID = "parent"
			e.Data.ReplacedMessages = 1
			e.Data.Messages = []providers.Message{{Role: "user", Content: "approved summary"}}
			e.Data.Compaction = &ContextCompaction{Version: 1, SummaryAttemptID: "attempt", SummaryReviewID: "review", SourceTaskID: "parent", SourceSequence: 2, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: ContextSummary{Decisions: []string{"retain"}}}
		}},
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

func TestEventCloneOwnsNestedStateAndCanonicalizesTime(t *testing.T) {
	usage := &providers.Usage{InputTokens: 2, OutputTokens: 3}
	event := Event{
		Version: 1, ID: "id", TaskID: "task", SessionID: "session",
		CorrelationID: "correlation", Sequence: 1,
		Time: time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("equivalent", -4*60*60)),
		Kind: TaskStarted,
		Data: Data{
			Messages:  []providers.Message{{Role: "user", Content: "owned"}},
			ToolCalls: []providers.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"value":"owned"}`)}},
			Usage:     usage,
		},
	}
	clone, err := event.Clone()
	if err != nil || clone.Time.Location() != time.UTC {
		t.Fatal(clone, err)
	}
	clone.Data.Messages[0].Content = "changed"
	clone.Data.ToolCalls[0].Arguments[0] = 'x'
	clone.Data.Usage.InputTokens = 99
	if event.Data.Messages[0].Content != "owned" || string(event.Data.ToolCalls[0].Arguments) != `{"value":"owned"}` || usage.InputTokens != 2 {
		t.Fatal("clone retained aliases")
	}
	if invalid, err := (Event{}).Clone(); err == nil || invalid.ID != "" {
		t.Fatal("invalid event cloned", invalid, err)
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
