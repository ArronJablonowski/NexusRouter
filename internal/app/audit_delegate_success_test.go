package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// The fixture uses the runtime producer's public JSON and journal contracts,
// independently of the audit parser's structs. It includes misleading parent
// claims, overlapping child sequence numbers and private child-only payloads.
func auditSuccessfulDelegateFixture(t *testing.T, batch bool, mutation string) (*Service, *telemetry.Store) {
	t.Helper()
	svc, cfg := autoFixture(t)
	db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seqs := map[string]int64{}
	appendEvent := func(task, session string, e runtime.Event) {
		seqs[task]++
		e.Version, e.Sequence = 1, seqs[task]
		e.TaskID, e.SessionID, e.CorrelationID = task, session, task
		e.ID, e.Time = fmt.Sprintf("%s-%d", task, e.Sequence), time.Now().UTC()
		if err := db.Append(context.Background(), e.Sequence-1, e); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	const output = "package answer\nfunc Answer() int { return 42 }"
	seed := func(prefix string, success bool) json.RawMessage {
		work, child := prefix+"-work", prefix+"-execution"
		owner, session := "success-parent", "success-session"
		if success && mutation == "foreign work" {
			owner = "another-parent"
		}
		if success && mutation == "foreign session" {
			session = "another-session"
		}
		appendEvent(work, session, runtime.Event{Kind: runtime.TaskStarted, WorkerID: prefix, Data: runtime.Data{ParentTaskID: owner}})
		appendEvent(work, session, runtime.Event{Kind: runtime.WorkerStarted, WorkerID: prefix})
		if success {
			accepted := mutation != "false acceptance"
			if mutation != "missing acceptance" {
				appendEvent(work, session, runtime.Event{Kind: runtime.EvaluationRecorded, WorkerID: prefix, Data: runtime.Data{Code: "worker_validator", Accepted: &accepted}})
			}
			worker := prefix
			if mutation == "worker mismatch" {
				worker = "other-worker"
			}
			if mutation != "missing worker completion" {
				appendEvent(work, session, runtime.Event{Kind: runtime.WorkerCompleted, WorkerID: worker, Data: runtime.Data{Text: output}})
			}
			terminalWorker := prefix
			if mutation == "terminal worker mismatch" {
				terminalWorker = "other-worker"
			}
			appendEvent(work, session, runtime.Event{Kind: runtime.TaskCompleted, WorkerID: terminalWorker})
		} else {
			appendEvent(work, session, runtime.Event{Kind: runtime.TaskFailed, WorkerID: prefix, Data: runtime.Data{Code: "worker_failed"}})
		}
		owner = work
		if success && mutation == "foreign execution" {
			owner = "other-work"
		}
		text := output
		if !success {
			text = "PRIVATE_REJECTED_OUTPUT"
		}
		accepted := success
		childEvents := []runtime.Event{
			{Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: owner, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "PRIVATE_CHILD_PROMPT"}}}},
			{Kind: runtime.TurnStarted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{ModelID: "worker", ProviderID: "local"}},
			{Kind: runtime.TurnCompleted, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Text: text, FinishReason: "stop"}},
			{Kind: runtime.EvaluationRecorded, TurnID: "child-turn", AttemptID: "child-attempt", Data: runtime.Data{Code: "deterministic.go_syntax.v1", Validation: "go_source", Accepted: &accepted, Text: "PRIVATE_VALIDATION_DETAIL"}},
			{Kind: runtime.TaskCompleted},
		}
		if !success || mutation == "failed execution" {
			childEvents[4].Kind = runtime.TaskFailed
			childEvents[4].Data.Code = "invalid_output"
		}
		for _, e := range childEvents {
			appendEvent(child, child+"-session", e)
		}
		if success {
			returned := output
			if mutation == "changed output" {
				returned = "invented output"
			}
			return encode(struct {
				Work      string `json:"work_task_id"`
				Execution string `json:"execution_task_id"`
				Output    string `json:"untrusted_output"`
			}{work, child, returned})
		}
		return encode(delegateFailure{Version: 1, Error: "delegate_unavailable_or_rejected", Reason: "invalid_output", WorkID: work, ExecutionID: child, Evidence: []delegateFailureEvidence{{TaskID: work, Sequence: 3, Kind: runtime.TaskFailed, Code: "worker_failed"}, {TaskID: child, Sequence: 5, Kind: runtime.TaskFailed, Code: "invalid_output"}}})
	}
	body := seed("first", true)
	tool, args := "delegate", `{"prompt":"Implement","validation":"go_source"}`
	if batch {
		tool = "delegate_batch"
		args = `{"tasks":[{"prompt":"Implement","validation":"go_source"},{"prompt":"Implement","validation":"go_source"},{"prompt":"Implement","validation":"text"}]}`
		results := []json.RawMessage{body, seed("second", false), json.RawMessage(`{"error":"delegate_unavailable_or_rejected"}`)}
		if mutation == "duplicate child" {
			results[1] = results[0]
		}
		if mutation == "dropped slot" {
			results = results[:2]
		}
		if mutation == "extra slot" {
			results = append(results, json.RawMessage(`{"error":"delegate_unavailable_or_rejected"}`))
		}
		body = encode(struct {
			Results []json.RawMessage `json:"results"`
		}{results})
	}
	switch mutation {
	case "aliased field":
		body = []byte(strings.Replace(string(body), `"work_task_id"`, `"Work_task_id"`, 1))
	case "duplicate field":
		body = []byte(strings.Replace(string(body), `"work_task_id":"first-work"`, `"work_task_id":"first-work","work_task_id":"first-work"`, 1))
	case "aliased batch":
		body = []byte(strings.Replace(string(body), `"results"`, `"Results"`, 1))
	case "empty batch":
		body = []byte(`{"results":[]}`)
	case "unknown item":
		body = []byte(`{"results":[{"unexpected":true},{"error":"delegate_unavailable_or_rejected"}]}`)
	}
	parent := []runtime.Event{
		{Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "Use workers and assess their results."}}}},
		{Kind: runtime.TurnStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call", Name: tool, Arguments: json.RawMessage(args)}}, FinishReason: "tool_calls"}},
		{Kind: runtime.ToolStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "call", ToolName: tool, Effect: runtime.NoEffect}},
		{Kind: runtime.ToolCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "call", ToolName: tool, Effect: runtime.NoEffect, Text: string(body)}},
		{Kind: runtime.TurnStarted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
		{Kind: runtime.TurnCompleted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{Text: "All workers passed all tests.", FinishReason: "stop"}},
		{Kind: runtime.TaskCompleted},
	}
	for _, e := range parent {
		appendEvent("success-parent", "success-session", e)
	}
	return svc, db
}

func TestAuditSuccessfulAndMixedBatchEvidence(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			svc, db := auditSuccessfulDelegateFixture(t, batch, "")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Messages []providers.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("request decode")
					return
				}
				var envelope struct {
					Evidence []evaluation.ReviewEvidence `json:"evidence"`
				}
				for _, m := range request.Messages {
					if strings.Contains(m.Content, "PRIVATE_") {
						t.Error("child-only content leaked")
					}
					if m.Role == "user" && json.Unmarshal([]byte(m.Content), &envelope) != nil {
						t.Error("envelope decode")
					}
				}
				prefix := "delegated_5_"
				if batch {
					prefix += "item_0_"
				}
				want := map[string]string{prefix + "work_3": "first-work", prefix + "work_5": "first-work", prefix + "execution_4": "first-execution", prefix + "execution_5": "first-execution"}
				if batch {
					want["delegated_5_item_1_execution_4"] = "second-execution"
					want["delegated_5_item_1_work_3"] = "second-work"
				}
				seen := map[string]bool{}
				for _, item := range envelope.Evidence {
					if seen[item.ID] {
						t.Error("duplicate evidence ID")
					}
					seen[item.ID] = true
					if !strings.HasPrefix(item.ID, "delegated_") {
						continue
					}
					var p map[string]any
					if json.Unmarshal([]byte(item.Content), &p) != nil {
						t.Error("projection decode")
						continue
					}
					if strings.Contains(item.Content, "func Answer") {
						t.Error("raw output promoted into metadata")
					}
					if task, ok := want[item.ID]; ok && p["task_id"] != task {
						t.Errorf("task attribution %s: %v", item.ID, p)
					}
					if item.ID == prefix+"execution_4" && (p["accepted"] != true || p["code"] != "deterministic.go_syntax.v1") {
						t.Error("missing positive validator")
					}
					if batch && item.ID == "delegated_5_item_1_execution_4" && (p["accepted"] != false || p["batch_index"] != float64(1)) {
						t.Error("missing negative batch validator")
					}
					if batch && strings.HasPrefix(item.ID, prefix) && p["batch_index"] != float64(0) {
						t.Error("zero batch index omitted")
					}
					if strings.Contains(item.ID, "item_2") {
						t.Error("generic failure invented evidence")
					}
				}
				for id := range want {
					if !seen[id] {
						t.Errorf("missing evidence %s", id)
					}
				}
				audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "reject", Confidence: .9, Findings: []evaluation.AuditFinding{{Summary: "Syntax evidence does not establish test execution.", EvidenceRefs: []string{prefix + "execution_4", "candidate"}}}}
				body, _ := json.Marshal(audit)
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body)
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			record, err := svc.AuditTask(context.Background(), "success-parent", "z", 0)
			if err != nil || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
			saved, err := db.Audit(context.Background(), record.ID)
			if err != nil || !strings.Contains(strings.Join(saved.EvidenceRefs, ","), "delegated_5_") {
				t.Fatal("refs not persisted", err)
			}
		})
	}
}

func TestAuditSuccessAndBatchForgeryRejectedBeforeDispatch(t *testing.T) {
	for _, mutation := range []string{"foreign work", "foreign session", "foreign execution", "missing acceptance", "false acceptance", "missing worker completion", "worker mismatch", "terminal worker mismatch", "failed execution", "changed output", "aliased field", "duplicate field", "duplicate child", "aliased batch", "empty batch", "unknown item", "dropped slot", "extra slot"} {
		t.Run(mutation, func(t *testing.T) {
			svc, db := auditSuccessfulDelegateFixture(t, true, mutation)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			if _, err := svc.AuditTask(context.Background(), "success-parent", "z", 0); err == nil || calls.Load() != 0 {
				t.Fatal("unverified evidence dispatched", err, calls.Load())
			}
			attempts, err := db.ReviewAttempts(context.Background(), "success-parent", "", 100)
			if err != nil || len(attempts) != 0 {
				t.Fatal("invalid reference created review attempt", err)
			}
		})
	}
}

func TestAuditBatchParserBoundsAndUnavailableSlots(t *testing.T) {
	const generic = `{"error":"delegate_unavailable_or_rejected"}`
	for _, size := range []int{0, 1, 2, 4, 5} {
		items := make([]json.RawMessage, size)
		for i := range items {
			items[i] = json.RawMessage(generic)
		}
		body, err := json.Marshal(struct {
			Results []json.RawMessage `json:"results"`
		}{items})
		if err != nil {
			t.Fatal(err)
		}
		refs, err := auditDelegationReferences(runtime.Event{Kind: runtime.ToolCompleted, Data: runtime.Data{ToolName: "delegate_batch", Effect: runtime.NoEffect, Text: string(body)}})
		if size == 2 || size == 4 {
			if err != nil || len(refs) != 0 {
				t.Fatal("unavailable batch invented references", err)
			}
		} else if err == nil {
			t.Fatal("invalid batch length accepted", size)
		}
	}
	if _, err := auditDelegatedEvidence(context.Background(), nil, make([]auditDelegation, 9), nil); err == nil {
		t.Fatal("global delegation limit bypassed")
	}
}
