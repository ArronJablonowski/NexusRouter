//go:build darwin || linux

package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestOrphanReadOnlyRecoveryAfterSIGKILL(t *testing.T) {
	for _, mode := range []string{"readonly_active", "readonly_complete"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, mode)
			before := orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || changed {
				t.Fatal("live owner recovered", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			kill()
			now := time.Now().UTC()
			plan, err := sessions.PlanInterruptedWorkerTree([][]runtime.Event{before["work"], before["child"]}, now)
			if err != nil || plan.Child == nil {
				t.Fatal("fixture not eligible", err)
			}
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, now); err != nil || !changed {
				t.Fatal("read-only child not recovered", changed, err)
			}
			after := orphanHistories(t, s)
			if !reflect.DeepEqual(before["parent"], after["parent"]) {
				t.Fatal("parent changed")
			}
			for _, task := range []string{"work", "child"} {
				want := plan.Worker.Events[0]
				if task == "child" {
					want = plan.Child.Events[0]
				}
				if len(after[task]) != len(before[task])+1 || !reflect.DeepEqual(before[task], after[task][:len(before[task])]) || !reflect.DeepEqual(want, after[task][len(before[task])]) {
					t.Fatal("noncanonical terminal or prefix drift", task)
				}
				if want.Kind != runtime.TaskFailed || want.Data.Accepted != nil || want.Data.Text != "" {
					t.Fatal("output accepted")
				}
			}
			if plan.Child.Events[0].Data.Code != "interrupted_read_only_model" {
				t.Fatal("wrong child code")
			}
			var body []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var receipt leaseRecoveryReceipt
			if json.Unmarshal(body, &receipt) != nil || receipt.ChildTaskID != "child" || receipt.ChildSequence != plan.Child.Events[0].Sequence || receipt.ChildEventID != plan.Child.Events[0].ID {
				t.Fatal("unbound child receipt")
			}
			for _, private := range []string{token, "private file content", "private candidate output", "darwin-owner-"} {
				if strings.Contains(string(body), private) {
					t.Fatal("receipt leaked evidence")
				}
			}
			for range 2 {
				if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || changed {
					t.Fatal("repeat recovery changed", changed, err)
				}
			}
			var repeat []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&repeat); err != nil || !reflect.DeepEqual(body, repeat) || !reflect.DeepEqual(after, orphanHistories(t, s)) {
				t.Fatal("idempotence drift", err)
			}
			var released int
			if err := s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released); err != nil || released != 1 {
				t.Fatal("reader retained", err)
			}
			// A receipt cannot name a different derived child terminal.
			receipt.ChildEventID = "forged-terminal"
			bad, _ := json.Marshal(receipt)
			if _, err := s.db.Exec(`UPDATE lease_recoveries SET body=? WHERE lease_token=?`, bad, token); err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
				t.Fatal("forged child receipt accepted")
			}
			if !reflect.DeepEqual(after, orphanHistories(t, s)) {
				t.Fatal("forgery changed journals")
			}
		})
	}
}

func TestOrphanReadOnlyRecoveryAtomicBoundaries(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "readonly_active")
	kill()
	before := orphanHistories(t, s)
	for _, boundary := range []string{
		`BEFORE INSERT ON events WHEN NEW.task_id='child'`,
		`BEFORE UPDATE OF state ON task_heads WHEN NEW.task_id='child'`,
		`BEFORE INSERT ON events WHEN NEW.task_id='work'`,
		`BEFORE UPDATE OF state ON task_heads WHEN NEW.task_id='work'`,
		`BEFORE INSERT ON lease_recoveries`,
		`BEFORE UPDATE OF released ON resource_leases`,
	} {
		if _, err := s.db.Exec(`CREATE TRIGGER reject_readonly ` + boundary + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
			t.Fatal(err)
		}
		changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now())
		if _, dropErr := s.db.Exec(`DROP TRIGGER reject_readonly`); dropErr != nil {
			t.Fatal(dropErr)
		}
		if err == nil || changed {
			t.Fatal("non-atomic mutation", boundary, changed, err)
		}
		assertOrphanUnchanged(t, s, before)
	}
	if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || !changed {
		t.Fatal("rollback not retryable", changed, err)
	}
}

func TestOrphanReadOnlyRecoveryRejectsAmbiguousToolsAndLease(t *testing.T) {
	for _, mode := range []string{"held_child_lease", "expired_child_lease", "legacy_start", "legacy_end", "write_start", "confirmed", "uncertain", "missing_terminal"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "readonly_active")
			kill()
			var err error
			switch mode {
			case "held_child_lease", "expired_child_lease":
				expires := time.Now().Add(time.Minute).UnixNano()
				if mode == "expired_child_lease" {
					expires = time.Now().Add(-time.Minute).UnixNano()
				}
				_, err = s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT 'child-token','child','child-owner','child-scope',0,?,0,process_id FROM resource_leases WHERE token=?`, expires, token)
			case "legacy_start":
				_, err = s.db.Exec(`UPDATE events SET body=json_remove(body,'$.data.tool_behavior') WHERE task_id='child' AND sequence=4`)
			case "legacy_end":
				_, err = s.db.Exec(`UPDATE events SET body=json_remove(body,'$.data.tool_behavior') WHERE task_id='child' AND sequence=5`)
			case "write_start":
				_, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.tool_behavior','idempotent_write') WHERE task_id='child' AND sequence=4`)
			case "confirmed", "uncertain":
				effect := runtime.ConfirmedEffect
				if mode == "uncertain" {
					effect = runtime.UncertainEffect
				}
				_, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.effect',?) WHERE task_id='child' AND sequence=5`, effect)
			case "missing_terminal":
				_, err = s.db.Exec(`DELETE FROM submission_stream_events WHERE task_id='child' AND task_sequence>=5; DELETE FROM events WHERE task_id='child' AND sequence>=5`)
				if err == nil {
					// Missing completion is now eligible only with an explicit
					// read-only dispatch. Legacy declarations remain unsafe.
					_, err = s.db.Exec(`UPDATE task_heads SET sequence=4 WHERE task_id='child'; UPDATE events SET body=json_remove(body,'$.data.tool_behavior') WHERE task_id='child' AND sequence=4`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := orphanHistories(t, s)
			if changed, _ := s.RecoverOrphanWorker(context.Background(), token, time.Now()); changed {
				t.Fatal("ambiguous read-only interruption recovered")
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanReadOnlyRecoveryRejectsCommitTimeChildLease(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "readonly_active")
	kill()
	before := orphanHistories(t, s)
	// A trigger simulates contradictory ownership appearing after the initial
	// check but before commit. The final ownership check must roll it all back.
	if _, err := s.db.Exec(`CREATE TRIGGER inject_readonly_lease AFTER INSERT ON lease_recoveries BEGIN INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT 'late-child-token','child','child-owner','child-scope',0,expires,0,process_id FROM resource_leases WHERE token=NEW.lease_token; END`); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
		t.Fatal("late child lease ignored", changed, err)
	}
	assertOrphanUnchanged(t, s, before)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM resource_leases WHERE task_id='child'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("trigger escaped rollback", count, err)
	}
}
