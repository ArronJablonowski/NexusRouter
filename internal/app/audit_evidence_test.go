package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func auditEvidenceEvent(seq int64, kind runtime.Kind) runtime.Event {
	accepted := false
	return runtime.Event{Version: 1, ID: "raw-event-id", TaskID: "task", SessionID: "session", CorrelationID: "correlation", Sequence: seq, Time: time.Now().UTC(), Kind: kind, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ToolCallID: "call", ToolName: "test", Effect: runtime.NoEffect, Code: "failed", Accepted: &accepted, Validation: "syntax", Text: "raw-output-not-evidence"}}
}

func TestAuditExecutionEvidenceProjection(t *testing.T) {
	events := []runtime.Event{auditEvidenceEvent(1, runtime.ModelDelta), auditEvidenceEvent(2, runtime.ToolCompleted), auditEvidenceEvent(3, runtime.EvaluationRecorded)}
	out, err := auditExecutionEvidence(events, nil)
	if err != nil || len(out) != 2 || out[0].ID != "execution_2" || out[1].ID != "execution_3" {
		t.Fatalf("projection: %+v %v", out, err)
	}
	for i, evidence := range out {
		var body map[string]any
		if json.Unmarshal([]byte(evidence.Content), &body) != nil || body["version"] != float64(1) || body["turn_id"] != "turn" || body["attempt_id"] != "attempt" || body["code"] != "failed" {
			t.Fatalf("invalid projection %s", evidence.Content)
		}
		if strings.Contains(evidence.Content, "raw-") || body["task_id"] != nil || body["text"] != nil {
			t.Fatalf("raw metadata escaped: %s", evidence.Content)
		}
		if i == 0 && (body["tool_call_id"] != "call" || body["effect"] != "none" || body["accepted"] != nil || body["validation"] != nil) {
			t.Fatalf("tool projection: %s", evidence.Content)
		}
		if i == 1 && (body["accepted"] != false || body["validation"] != "syntax" || body["tool_name"] != nil || body["effect"] != nil) {
			t.Fatalf("evaluation projection: %s", evidence.Content)
		}
	}
	empty, err := auditExecutionEvidence(nil, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty projection must be nonnil")
	}
}

func TestAuditExecutionEvidenceRejectsInvalidInput(t *testing.T) {
	for name, mutate := range map[string]func(*runtime.Event){
		"sequence": func(e *runtime.Event) { e.Sequence = 0 },
		"version":  func(e *runtime.Event) { e.Version = 2 },
		"turn":     func(e *runtime.Event) { e.TurnID = "" },
		"attempt":  func(e *runtime.Event) { e.AttemptID = "" },
		"effect":   func(e *runtime.Event) { e.Data.Effect = "invalid" },
		"oversize": func(e *runtime.Event) { e.Data.Code = strings.Repeat("x", 64<<10) },
	} {
		t.Run(name, func(t *testing.T) {
			e := auditEvidenceEvent(1, runtime.ToolCompleted)
			mutate(&e)
			if out, err := auditExecutionEvidence([]runtime.Event{e}, nil); !errors.Is(err, ErrAdmission) || out != nil {
				t.Fatalf("accepted malformed evidence: %+v %v", out, err)
			}
		})
	}
	for _, field := range []string{"task", "session", "sequence", "accepted"} {
		events := []runtime.Event{auditEvidenceEvent(1, runtime.ToolCompleted), auditEvidenceEvent(2, runtime.EvaluationRecorded)}
		switch field {
		case "task":
			events[1].TaskID = "other"
		case "session":
			events[1].SessionID = "other"
		case "sequence":
			events[1].Sequence = 1
		case "accepted":
			events[1].Data.Accepted = nil
		}
		if out, err := auditExecutionEvidence(events, nil); !errors.Is(err, ErrAdmission) || out != nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
}

func TestAuditExecutionEvidenceBoundsAndRedaction(t *testing.T) {
	events := make([]runtime.Event, 251)
	for i := range events {
		events[i] = auditEvidenceEvent(int64(i+1), runtime.ToolCompleted)
	}
	if _, err := auditExecutionEvidence(events[:250], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := auditExecutionEvidence(events, nil); !errors.Is(err, ErrAdmission) {
		t.Fatal("record limit ignored")
	}
	for i := range events[:250] {
		events[i].Data.Code = strings.Repeat("x", 300)
	}
	if _, err := auditExecutionEvidence(events[:250], nil); !errors.Is(err, ErrAdmission) {
		t.Fatal("aggregate byte limit ignored")
	}
	secret := "token\"\\\nvalue"
	e := auditEvidenceEvent(1, runtime.EvaluationRecorded)
	e.Data.Validation, e.Data.Code, e.TurnID, e.AttemptID = secret, secret, secret, secret
	out, err := auditExecutionEvidence([]runtime.Event{e}, []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal([]byte(out[0].Content), &body) != nil {
		t.Fatal("invalid redacted JSON")
	}
	for _, field := range []string{"validation", "code", "turn_id", "attempt_id"} {
		if body[field] != "[REDACTED]" {
			t.Fatalf("escaped credential leaked in %s", field)
		}
	}
}
