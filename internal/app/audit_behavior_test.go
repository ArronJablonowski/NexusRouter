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

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestAuditTaskPreservesDeclaredToolBehaviorInReviewerEvidence(t *testing.T) {
	for _, mode := range []string{"read_only", "idempotent_write", "non_idempotent_write", "delegated_read_only"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			behavior := runtime.ToolBehavior(mode)
			var svc *Service
			var db *telemetry.Store
			task := "behavior-task"
			if mode == "delegated_read_only" {
				behavior = runtime.BehaviorReadOnly
				svc, db = auditSuccessfulDelegateFixture(t, false, "")
				task = "success-parent"
				// Match both ends of the already paired delegation, as produced by
				// the runtime. This is metadata, not a new execution or authority.
				changed := rewriteCanonicalAppEventsForTest(t, svc.settings.Telemetry.Database, task, func(event runtime.Event) bool {
					return event.Kind == runtime.ToolStarted || event.Kind == runtime.ToolCompleted
				}, func(event *runtime.Event) { event.Data.ToolBehavior = behavior })
				if changed != 2 {
					t.Fatalf("changed %d delegation events", changed)
				}
			} else {
				var err error
				svc, _ = autoFixture(t)
				db, err = telemetry.Open(ctx, svc.settings.Telemetry.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				effect := runtime.NoEffect
				if behavior != runtime.BehaviorReadOnly {
					effect = runtime.ConfirmedEffect
				}
				// Seed a historical operation. AuditTask must load and preserve its
				// declared class without executing the operation or granting tools.
				events := []runtime.Event{
					{Kind: runtime.TaskStarted, Data: runtime.Data{Domain: "code", Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "Review the observed operation."}}}},
					{Kind: runtime.TurnStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
					{Kind: runtime.TurnCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "operation-call", Name: "operation", Arguments: json.RawMessage(`{}`)}}}},
					{Kind: runtime.ToolStarted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "operation-call", ToolName: "operation", ToolBehavior: behavior, Effect: runtime.UncertainEffect}},
					{Kind: runtime.ToolCompleted, TurnID: "tool-turn", AttemptID: "tool-attempt", Data: runtime.Data{ToolCallID: "operation-call", ToolName: "operation", ToolBehavior: behavior, Effect: effect, Text: "untrusted operation result"}},
					{Kind: runtime.TurnStarted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{ModelID: "a", ProviderID: "local"}},
					{Kind: runtime.TurnCompleted, TurnID: "final-turn", AttemptID: "final-attempt", Data: runtime.Data{Text: "Observed operation completed.", FinishReason: "stop"}},
					{Kind: runtime.TaskCompleted},
				}
				for i, e := range events {
					e.Version = 1
					e.ID = fmt.Sprintf("behavior-%d", i)
					e.Sequence = int64(i + 1)
					e.TaskID = task
					e.SessionID = "behavior-session"
					e.CorrelationID = task
					e.Time = time.Now().UTC()
					if err = db.Append(ctx, int64(i), e); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := db.Read(ctx, task, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "delegated_read_only" {
				refs, err := auditDelegationReferences(before[4])
				if err != nil || len(refs) != 1 || refs[0].parent.Data.ToolBehavior != behavior {
					t.Fatal("delegation parser discarded retained class")
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Messages []providers.Message `json:"messages"`
					Tools    []json.RawMessage   `json:"tools"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("review request malformed")
					return
				}
				if len(request.Tools) != 0 {
					t.Error("behavior evidence granted reviewer tool authority")
				}
				var envelope struct {
					Evidence []evaluation.ReviewEvidence `json:"evidence"`
				}
				for _, message := range request.Messages {
					if message.Role == "user" {
						if json.Unmarshal([]byte(message.Content), &envelope) != nil {
							t.Error("review evidence unavailable")
						}
					}
				}
				found, delegated := false, false
				for _, item := range envelope.Evidence {
					delegated = delegated || strings.HasPrefix(item.ID, "delegated_")
					if item.ID != "execution_5" {
						continue
					}
					var projected map[string]json.RawMessage
					if json.Unmarshal([]byte(item.Content), &projected) != nil {
						t.Error("execution projection malformed")
						continue
					}
					var actual runtime.ToolBehavior
					if json.Unmarshal(projected["tool_behavior"], &actual) != nil || actual != behavior {
						t.Error("AuditTask dropped operation class before reviewer dispatch")
					} else {
						found = true
					}
					if projected["arguments"] != nil || projected["text"] != nil {
						t.Error("operation payload promoted to execution metadata")
					}
				}
				if !found || (mode == "delegated_read_only" && !delegated) {
					t.Error("missing declared behavior or independently loaded delegation evidence")
				}
				audit := evaluation.Audit{Version: 1, EvaluatorID: "z", RubricVersion: "darwin-review-v2", Domain: "code", Verdict: "abstain", Confidence: 0, Findings: []evaluation.AuditFinding{}}
				body, _ := json.Marshal(audit)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(body)}, "done": true, "done_reason": "stop"})
			}))
			defer server.Close()
			svc.settings.Providers[0].Endpoint = server.URL
			record, err := svc.AuditTask(ctx, task, "z", 0)
			if err != nil || calls.Load() != 1 || record.ID == "" {
				t.Fatal("production audit failed", err, calls.Load())
			}
			after, err := db.Read(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("audit mutated source class or history", err)
			}
			saved, err := db.Audit(ctx, record.ID)
			if err != nil || saved.TaskID != task {
				t.Fatal("advisory review not persisted", err)
			}
		})
	}
}
