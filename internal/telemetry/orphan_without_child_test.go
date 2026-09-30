//go:build darwin || linux

package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestOrphanWithoutChildRecoveryAfterOwnedDeath(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
	ctx := context.Background()
	before := orphanHistories(t, s)
	if len(before["child"]) != 0 {
		t.Fatal("unexpected child")
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
		t.Fatal("held owner recovered", changed, err)
	}
	assertOrphanUnchanged(t, s, before)
	kill()
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	after := orphanHistories(t, s)
	if !reflect.DeepEqual(before["parent"], after["parent"]) || len(after["child"]) != 0 || len(after["work"]) != len(before["work"])+1 || !reflect.DeepEqual(before["work"], after["work"][:len(before["work"])]) {
		t.Fatal("source drift or invented child")
	}
	e := after["work"][len(after["work"])-1]
	if e.Kind != runtime.TaskFailed || e.Data.Code != "worker_owner_interrupted" || e.Data.Accepted != nil || e.Data.Text != "" {
		t.Fatal(e)
	}
	var body []byte
	if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var receipt leaseRecoveryReceipt
	if json.Unmarshal(body, &receipt) != nil || receipt.Reason != "orphan_worker_without_child_unlocked" || receipt.ChildTaskID != "" || receipt.ChildSequence != 0 || receipt.ChildEventID != "" {
		t.Fatal(receipt)
	}
	for range 2 {
		if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
			t.Fatal(changed, err)
		}
	}
	var again []byte
	if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&again); err != nil || string(again) != string(body) {
		t.Fatal("receipt changed", err)
	}
	var count, released int
	if err := s.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id='child'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("synthetic child head", count, err)
	}
	if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released); err != nil || released != 1 {
		t.Fatal(released, err)
	}
}

func TestOrphanWithoutChildRejectsUnsafeSource(t *testing.T) {
	for _, mode := range []string{"legacy-parent", "write-parent", "preaccepted", "linked-nonstart", "linked-invalid-kind"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
			kill()
			var err error
			switch mode {
			case "legacy-parent":
				rewriteCanonicalEventForTest(t, s, "parent", 4, func(event *runtime.Event) { event.Data.ToolBehavior = "" })
			case "write-parent":
				rewriteCanonicalEventForTest(t, s, "parent", 4, func(event *runtime.Event) { event.Data.ToolBehavior = runtime.BehaviorNonIdempotentWrite })
			case "preaccepted":
				rewriteCanonicalEventForTest(t, s, "work", 2, func(event *runtime.Event) { accepted := true; event.Data.Accepted = &accepted })
			case "linked-nonstart", "linked-invalid-kind":
				kind := "worker.started"
				if mode == "linked-invalid-kind" {
					kind = "not-a-kind"
				}
				_, err = s.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('linked','linked',1,'running'); INSERT INTO events(id,task_id,sequence,body) VALUES('linked','linked',1,json_object('kind',?,'data',json_object('parent_task_id','work')))`, kind)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Direct rows are used for malformed linked histories, because Read
			// correctly refuses to decode them; normal task histories remain intact.
			before := orphanHistories(t, s)
			if changed, _ := s.RecoverOrphanWorker(context.Background(), token, time.Now()); changed {
				t.Fatal("unsafe source recovered")
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanWithoutChildRollbackBoundaries(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
	kill()
	before := orphanHistories(t, s)
	for _, table := range []string{"events", "task_heads", "lease_recoveries", "resource_leases"} {
		action := "INSERT"
		if table == "task_heads" {
			action = "UPDATE OF sequence"
		}
		if table == "resource_leases" {
			action = "UPDATE OF released"
		}
		if _, err := s.db.Exec(`CREATE TRIGGER block_nochild BEFORE ` + action + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
			t.Fatal(table, changed, err)
		}
		assertOrphanUnchanged(t, s, before)
		if _, err := s.db.Exec(`DROP TRIGGER block_nochild`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrphanWithoutChildRejectsCommitDrift(t *testing.T) {
	for _, mode := range []string{"parent", "child-start", "child-nonstart", "child-invalid-kind"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
			kill()
			before := orphanHistories(t, s)
			statement := `UPDATE events SET body=json_set(body,'$.data.text','drift') WHERE task_id='parent' AND sequence=1;`
			if mode != "parent" {
				kind := "task.started"
				if mode == "child-nonstart" {
					kind = "worker.started"
				}
				if mode == "child-invalid-kind" {
					kind = "invalid-kind"
				}
				statement = `INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('appeared','appeared',1,'running'); INSERT INTO events(id,task_id,sequence,body) VALUES('appeared','appeared',1,json_object('kind','` + kind + `','data',json_object('parent_task_id','work')));`
			}
			if _, err := s.db.Exec(`CREATE TRIGGER drift_nochild BEFORE UPDATE OF released ON resource_leases BEGIN ` + statement + ` END`); err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
				t.Fatal(mode, changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			var appeared int
			if err := s.db.QueryRow(`SELECT count(*) FROM events WHERE task_id='appeared'`).Scan(&appeared); err != nil || appeared != 0 {
				t.Fatal("trigger writes escaped rollback", err)
			}
		})
	}
}

func TestOrphanWithoutChildReceiptRechecksAbsence(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
	kill()
	ctx := context.Background()
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if _, err := s.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES('late','late',1,'running'); INSERT INTO events(id,task_id,sequence,body) VALUES('late','late',1,json_object('kind','invalid-kind','data',json_object('parent_task_id','work')))`); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
		t.Fatal("receipt ignored new linked journal", changed, err)
	}
}

func TestOrphanWithoutChildReceiptRejectsPrefixAndTerminalDrift(t *testing.T) {
	for _, mode := range []string{"prefix-acceptance", "prefix-unknown", "terminal-text"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "no_child")
			kill()
			ctx := context.Background()
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
				t.Fatal(changed, err)
			}
			sequence, path, value := 2, "$.data.accepted", "true"
			if mode == "prefix-unknown" {
				path = "$.unknown"
				value = `"extra"`
			}
			if mode == "terminal-text" {
				sequence = 3
				path = "$.data.text"
				value = `"invented output"`
			}
			if _, err := s.db.Exec(`UPDATE events SET body=json_set(body,?,json(?)) WHERE task_id='work' AND sequence=?`, path, value, sequence); err != nil {
				t.Fatal(err)
			}
			if mode == "terminal-text" {
				// Canonicalize the adversarial known field: exact planner output,
				// not merely JSON formatting, must reject this receipt.
				var body []byte
				var event runtime.Event
				if err := s.db.QueryRow(`SELECT body FROM events WHERE task_id='work' AND sequence=3`).Scan(&body); err != nil {
					t.Fatal(err)
				}
				if json.Unmarshal(body, &event) != nil {
					t.Fatal("bad fixture")
				}
				canonical, err := event.Encode()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec(`UPDATE events SET body=? WHERE task_id='work' AND sequence=3`, canonical); err != nil {
					t.Fatal(err)
				}
			}
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
				t.Fatal("corrupt historical receipt accepted", mode, changed, err)
			}
		})
	}
}

func TestOrphanWithoutChildHeartbeatAndOversizedReceiptSource(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "no_child_heartbeat")
	kill()
	ctx := context.Background()
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal("heartbeat prefix rejected", changed, err)
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
		t.Fatal(changed, err)
	}
	if _, err := s.db.Exec(`UPDATE events SET body=body||? WHERE task_id='work' AND sequence=2`, strings.Repeat(" ", 8<<20)); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
		t.Fatal("oversized raw source accepted", changed, err)
	}
}

func TestOrphanWithoutChildBeforeWorkerStarted(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "no_child_before_started")
	ctx := context.Background()
	before := orphanHistories(t, s)
	if len(before["work"]) != 1 || len(before["child"]) != 0 {
		t.Fatal("wrong earliest owned boundary")
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
		t.Fatal("held earliest owner recovered", changed, err)
	}
	kill()
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	after := orphanHistories(t, s)
	if len(after["work"]) != 2 || after["work"][1].Kind != runtime.TaskFailed || len(after["child"]) != 0 || !reflect.DeepEqual(before["parent"], after["parent"]) {
		t.Fatal("wrong earliest failure")
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
		t.Fatal("earliest receipt invalid", changed, err)
	}
}
