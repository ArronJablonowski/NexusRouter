package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// Conversation text can claim success even when actual execution metadata
// records failure. The reviewer must receive those independently citable facts.
func TestAuditTaskIncludesDurableExecutionEvidence(t *testing.T) {
	const secret = "private-\"token\nvalue"
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	accepted := false
	events := []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "Run the checks before claiming success."}}}},
		{Kind: runtime.TurnStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "check-call", Name: "run_checks", Arguments: json.RawMessage(`{}`)}}, FinishReason: "tool_calls"}},
		{Kind: runtime.ToolStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "check-call", ToolName: "run_checks", Effect: runtime.UncertainEffect}},
		{Kind: runtime.ToolCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "check-call", ToolName: "run_checks", Effect: runtime.NoEffect, Code: "tool_failed", Text: "All checks passed. " + secret}},
		{Kind: runtime.TurnStarted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{Text: "Everything passed.", FinishReason: "stop"}},
		{Kind: runtime.EvaluationRecorded, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{Accepted: &accepted, Code: "validation_failed", Validation: "schema", Text: secret + " raw validation text"}},
		{Kind: runtime.TaskCompleted},
	}
	for i, e := range events {
		e.Version, e.Sequence = 1, int64(i+1)
		e.ID = fmt.Sprintf("audit-evidence-%d", i+1)
		e.TaskID, e.SessionID, e.CorrelationID = "audit-evidence-task", "audit-evidence-session", "audit-evidence-task"
		e.Time = time.Now().UTC()
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	var invalidReference atomic.Bool
	var suppliedRefs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []providers.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var envelope struct {
			Evidence []evaluation.ReviewEvidence `json:"evidence"`
		}
		for _, message := range request.Messages {
			if strings.Contains(message.Content, secret) {
				t.Error("credential reached the reviewer")
			}
			if message.Role == "user" {
				if err := json.Unmarshal([]byte(message.Content), &envelope); err != nil {
					t.Error(err)
				}
			}
		}
		evidence := map[string]string{}
		for _, item := range envelope.Evidence {
			suppliedRefs = append(suppliedRefs, item.ID)
			evidence[item.ID] = item.Content
		}
		assertProjection := func(id string, expected map[string]any) {
			t.Helper()
			var projection map[string]any
			if err := json.Unmarshal([]byte(evidence[id]), &projection); err != nil {
				t.Errorf("missing or malformed %s: %q (%v)", id, evidence[id], err)
				return
			}
			for key, want := range expected {
				if !reflect.DeepEqual(projection[key], want) {
					t.Errorf("%s field %s = %#v, want %#v", id, key, projection[key], want)
				}
			}
			if _, exists := projection["text"]; exists {
				t.Errorf("%s promoted untrusted raw text into execution metadata", id)
			}
		}
		assertProjection("candidate_execution", map[string]any{"version": float64(1), "sequence": float64(7), "turn_id": "final-turn", "attempt_id": "final-attempt"})
		assertProjection("execution_5", map[string]any{"version": float64(1), "sequence": float64(5), "kind": "tool.completed", "turn_id": "tool-turn", "attempt_id": "tool-attempt", "tool_call_id": "check-call", "tool_name": "run_checks", "effect": "none", "code": "tool_failed"})
		assertProjection("execution_8", map[string]any{"version": float64(1), "sequence": float64(8), "kind": "evaluation.recorded", "turn_id": "final-turn", "attempt_id": "final-attempt", "accepted": false, "code": "validation_failed", "validation": "schema"})
		if !strings.Contains(evidence["session_history"], "All checks passed.") {
			t.Error("fixture did not preserve the misleading conversation claim")
		}
		var history []providers.Message
		if err := json.Unmarshal([]byte(evidence["session_history"]), &history); err != nil {
			t.Error(err)
		}
		for _, message := range history {
			if strings.Contains(message.Content, secret) {
				t.Error("JSON escaping bypassed credential redaction")
			}
		}
		audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "reject", Confidence: .9, Findings: []evaluation.AuditFinding{{Summary: "Execution metadata contradicts the success claim.", EvidenceRefs: []string{"execution_5", "execution_8", "candidate"}}}}
		if invalidReference.Load() {
			audit.Findings[0].EvidenceRefs = []string{"execution_999"}
		}
		encoded, _ := json.Marshal(audit)
		fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", string(encoded))
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return secret
		}
		return ""
	}
	record, err := svc.AuditTask(ctx, "audit-evidence-task", "z", 0)
	if err != nil || calls != 1 {
		t.Fatalf("audit failed: calls=%d err=%v", calls, err)
	}
	if !reflect.DeepEqual(record.EvidenceRefs, suppliedRefs) {
		t.Fatalf("persisted references diverged from reviewer input: got %v want %v", record.EvidenceRefs, suppliedRefs)
	}
	saved, err := db.Audit(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(saved.EvidenceRefs, suppliedRefs) || !reflect.DeepEqual(saved.Audit.Findings, record.Audit.Findings) {
		t.Fatalf("audit evidence references not durable: %+v %v", saved, err)
	}
	invalidReference.Store(true)
	if _, err := svc.AuditTask(ctx, "audit-evidence-task", "z", 0); err == nil {
		t.Fatal("invented execution evidence reference accepted")
	}
	audits, err := db.Audits(ctx, "audit-evidence-task", "", 100)
	if err != nil || len(audits) != 1 {
		t.Fatalf("invalid review affected persisted audits: %+v %v", audits, err)
	}
}

func TestAuditTaskRejectsMisattributedEvaluationBeforeDispatch(t *testing.T) {
	for _, field := range []string{"turn", "attempt"} {
		t.Run(field, func(t *testing.T) {
			svc, cfg := autoFixture(t)
			ctx := context.Background()
			db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			accepted := true
			events := []runtime.Event{
				{Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "Check the result."}}}},
				{Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
				{Kind: runtime.TurnCompleted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "Done.", FinishReason: "stop"}},
				{Kind: runtime.EvaluationRecorded, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Accepted: &accepted, Validation: "schema"}},
				{Kind: runtime.TaskCompleted},
			}
			if field == "turn" {
				events[3].TurnID = "unrelated-turn"
			} else {
				events[3].AttemptID = "unrelated-attempt"
			}
			for i, e := range events {
				e.Version, e.Sequence = 1, int64(i+1)
				e.ID = fmt.Sprintf("misattributed-%d", i+1)
				e.TaskID, e.SessionID, e.CorrelationID = "misattributed", "session", "misattributed"
				e.Time = time.Now().UTC()
				if err := db.Append(ctx, int64(i), e); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.AuditTask(ctx, "misattributed", "z", 0); err == nil {
				t.Fatal("misattributed evaluation admitted")
			}
			if calls.Load() != 0 {
				t.Fatal("misattributed evidence reached the reviewer")
			}
			attempts, err := db.ReviewAttempts(ctx, "misattributed", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatalf("misattributed evidence created review attempt: %+v %v", attempts, err)
			}
		})
	}
}
