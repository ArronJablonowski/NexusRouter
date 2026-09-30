//go:build darwin || linux

package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// This mutation is confined to the owned, stopped fixture database. It models
// a crash after a partial model stream instead of after TurnCompleted.
func orphanChildStreamPrefix(t *testing.T, s *Store) {
	t.Helper()
	rewriteCanonicalEventForTest(t, s, "child", 3, func(event *runtime.Event) {
		event.Kind = runtime.ModelDelta
		event.Data.FinishReason = ""
	})
}

func assertRecoveredOrphanChild(t *testing.T, s *Store, token string, before map[string][]runtime.Event, interrupted bool) []byte {
	t.Helper()
	after := orphanHistories(t, s)
	if !reflect.DeepEqual(before["parent"], after["parent"]) {
		t.Fatal("parent journal changed")
	}
	for _, task := range []string{"work", "child"} {
		if len(after[task]) != len(before[task])+1 || !reflect.DeepEqual(before[task], after[task][:len(before[task])]) {
			t.Fatal("source prefix changed", task)
		}
		last := after[task][len(after[task])-1]
		code := "worker_owner_interrupted"
		if task == "child" {
			code = "interrupted_model"
		}
		if last.Kind != runtime.TaskFailed || last.Data.Code != code || last.Data.Accepted != nil || last.Data.Text != "" {
			t.Fatal("recovery accepted output or wrong terminal", task, last)
		}
		snapshot, err := s.TaskSnapshot(context.Background(), task)
		if err != nil || snapshot.State != "failed" || len(snapshot.Pending) != 0 || snapshot.UncertainEffects {
			t.Fatal("invalid recovered projection", task, snapshot, err)
		}
		if task == "child" && snapshot.InterruptedTurn != interrupted {
			t.Fatal("interrupted turn evidence was lost", snapshot.InterruptedTurn)
		}
	}
	var released, receipts int
	if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released); err != nil || released != 1 {
		t.Fatal("reader not released", released, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("receipt count", receipts, err)
	}
	var body []byte
	if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		ChildTask     string `json:"child_task_id"`
		ChildSequence int64  `json:"child_sequence"`
		ChildEvent    string `json:"child_event_id"`
	}
	child := after["child"][len(after["child"])-1]
	if json.Unmarshal(body, &receipt) != nil || receipt.ChildTask != "child" || receipt.ChildSequence != child.Sequence || receipt.ChildEvent != child.ID {
		t.Fatal("receipt does not bind child repair", string(body))
	}
	for _, private := range []string{token, "private candidate output", "darwin-owner-"} {
		if strings.Contains(string(body), private) {
			t.Fatal("sensitive recovery receipt")
		}
	}
	return body
}

func TestOrphanChildRecoveryAfterSIGKILL(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "completed_turn"
		if partial {
			name = "partial_stream"
		}
		t.Run(name, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "active")
			ctx := context.Background()
			before := orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
				t.Fatal("held owner recovered", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			kill()
			if partial {
				orphanChildStreamPrefix(t, s)
			}
			before = orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
				t.Fatal("stopped model-only child not recovered", changed, err)
			}
			body := assertRecoveredOrphanChild(t, s, token, before, partial)
			after := orphanHistories(t, s)
			for i := 0; i < 2; i++ {
				if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
					t.Fatal("repeat recovery", changed, err)
				}
			}
			var repeated []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&repeated); err != nil || !reflect.DeepEqual(body, repeated) || !reflect.DeepEqual(after, orphanHistories(t, s)) {
				t.Fatal("repeat changed durable result", err)
			}
		})
	}
}

func TestOrphanChildRecoveryAtomicBoundaries(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "active")
	kill()
	orphanChildStreamPrefix(t, s)
	before := orphanHistories(t, s)
	for _, boundary := range []struct{ name, trigger string }{
		{"child_event", `BEFORE INSERT ON events WHEN NEW.task_id='child'`},
		{"child_head", `BEFORE UPDATE OF state ON task_heads WHEN NEW.task_id='child'`},
		{"worker_event", `BEFORE INSERT ON events WHEN NEW.task_id='work'`},
		{"worker_head", `BEFORE UPDATE OF state ON task_heads WHEN NEW.task_id='work'`},
		{"receipt", `BEFORE INSERT ON lease_recoveries`},
		{"release", `BEFORE UPDATE OF released ON resource_leases`},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			if _, err := s.db.Exec(`CREATE TRIGGER reject_child ` + boundary.trigger + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := s.db.Exec(`DROP TRIGGER IF EXISTS reject_child`); err != nil {
					t.Error(err)
				}
			})
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
				t.Fatal("non-atomic recovery", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			for _, task := range []string{"work", "child"} {
				var state string
				var seq int64
				if err := s.db.QueryRow(`SELECT state,sequence FROM task_heads WHERE task_id=?`, task).Scan(&state, &seq); err != nil || state != "running" || seq != int64(len(before[task])) {
					t.Fatal("projection escaped rollback", task, state, seq, err)
				}
			}
			if _, err := s.db.Exec(`DROP TRIGGER reject_child`); err != nil {
				t.Fatal(err)
			}
		})
	}
	if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || !changed {
		t.Fatal("retry after rollback", changed, err)
	}
	assertRecoveredOrphanChild(t, s, token, before, true)
}

func TestOrphanChildRecoveryConcurrentConnections(t *testing.T) {
	s, path, token, kill := startOrphanWorkerOwner(t, "active")
	kill()
	orphanChildStreamPrefix(t, s)
	before := orphanHistories(t, s)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := store.RecoverOrphanWorker(context.Background(), token, time.Now())
			results <- result{changed, err}
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.changed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("multiple recovery commits", winners)
	}
	assertRecoveredOrphanChild(t, s, token, before, true)
}

func TestOrphanChildRecoveryRejectsNonModelEvidence(t *testing.T) {
	for _, mode := range []string{"tool_call", "evaluation", "wrong_terminal_code", "child_lease"} {
		t.Run(mode, func(t *testing.T) {
			helper := "active"
			if mode == "wrong_terminal_code" {
				helper = "interrupted"
			}
			s, _, token, kill := startOrphanWorkerOwner(t, helper)
			kill()
			var err error
			switch mode {
			case "child_lease":
				_, err = s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT 'child-token','child','child-owner','child-scope',0,expires,0,process_id FROM resource_leases WHERE token=?`, token)
			case "tool_call":
				rewriteCanonicalEventForTest(t, s, "child", 3, func(event *runtime.Event) {
					event.Data.ToolCalls = []providers.ToolCall{{ID: "effect", Name: "tool", Arguments: json.RawMessage(`{}`)}}
					event.Data.FinishReason = "tool_calls"
				})
			case "evaluation":
				history := orphanHistories(t, s)["child"]
				e := history[len(history)-1]
				e.ID, e.Sequence, e.Kind = "child-4", 4, runtime.EvaluationRecorded
				e.Time = e.Time.Add(time.Millisecond)
				yes := true
				e.Data = runtime.Data{ProviderID: "fixture", ModelID: "model", Accepted: &yes, Code: "deterministic.nonempty_text.v1"}
				insertCanonicalEventForTest(t, s, e, "running")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := orphanHistories(t, s)
			if changed, _ := s.RecoverOrphanWorker(context.Background(), token, time.Now()); changed {
				t.Fatal("non-model evidence admitted")
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanChildRecoveryReceiptBindsChild(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "active")
	kill()
	before := orphanHistories(t, s)
	if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	body := assertRecoveredOrphanChild(t, s, token, before, false)
	after := orphanHistories(t, s)
	for _, field := range []string{"child_task_id", "child_sequence", "child_event_id"} {
		var receipt leaseRecoveryReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "child_sequence":
			receipt.ChildSequence = 999
		case "child_task_id":
			receipt.ChildTaskID = "different"
		case "child_event_id":
			receipt.ChildEventID = "different"
		}
		bad, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE lease_recoveries SET body=? WHERE lease_token=?`, bad, token); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
			t.Fatal("conflicting child receipt accepted", field, changed, err)
		}
		if !reflect.DeepEqual(after, orphanHistories(t, s)) {
			t.Fatal("receipt conflict changed history")
		}
	}
}
