//go:build darwin || linux

package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestOrphanPendingToolsOwnedDeathAndSeparateReaderSweep(t *testing.T) {
	for _, mode := range []string{"readtool_pending", "readtool_pending_released", "readtool_pending_reader"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, mode)
			ctx := context.Background()
			before := orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
				t.Fatal("held owner recovered", changed, err)
			}
			kill()
			if _, count, err := s.RecoverTerminalReadersPage(ctx, "", 64, time.Now()); err != nil || count != 0 {
				t.Fatal("reader recovered before resolved child", count, err)
			}
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
				t.Fatal(changed, err)
			}
			after := orphanHistories(t, s)
			if !reflect.DeepEqual(before["parent"], after["parent"]) || len(after["child"]) != len(before["child"])+2 || len(after["work"]) != len(before["work"])+1 || !reflect.DeepEqual(before["child"], after["child"][:len(before["child"])]) {
				t.Fatal("wrong suffix or changed source")
			}
			tool, last := after["child"][len(before["child"])], after["child"][len(before["child"])+1]
			if tool.Kind != runtime.ToolCompleted || tool.Data.ToolBehavior != runtime.BehaviorReadOnly || tool.Data.Effect != runtime.NoEffect || tool.Data.Code != "tool_failed" || last.Kind != runtime.TaskFailed || last.Data.Code != "interrupted_read_only_tool" || last.CausationID != before["child"][len(before["child"])-1].ID {
				t.Fatal(tool, last)
			}
			var receipt []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			var r leaseRecoveryReceipt
			if json.Unmarshal(receipt, &r) != nil || r.ChildSequence != last.Sequence || r.ChildEventID != last.ID {
				t.Fatal(r)
			}
			var held int
			if err := s.db.QueryRow(`SELECT count(*) FROM resource_leases WHERE task_id='child' AND released=0`).Scan(&held); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "readtool_pending_reader" {
				want = 1
			}
			if held != want {
				t.Fatal("worker recovery changed child reader", held, want)
			}
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
				t.Fatal(changed, err)
			}
			if _, count, err := s.RecoverTerminalReadersPage(ctx, "", 64, time.Now()); err != nil || count != want {
				t.Fatal("independent reader sweep failed", count, err)
			}
			var again []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&again); err != nil || string(again) != string(receipt) {
				t.Fatal("worker receipt changed", err)
			}
		})
	}
}

func TestOrphanPendingToolsRejectsUnsafeReaders(t *testing.T) {
	for _, mode := range []string{"writer", "foreign", "legacy", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			fixture := "readtool_pending_reader"
			if mode == "writer" {
				fixture = "readtool_pending_writer"
			}
			s, _, token, kill := startOrphanWorkerOwner(t, fixture)
			kill()
			ctx := context.Background()
			var err error
			if mode == "foreign" {
				_, err = s.AcquireLease(ctx, "child", "foreign-owner", "foreign-scope", false, time.Now(), time.Minute)
			}
			if mode == "legacy" {
				_, err = s.db.Exec(`UPDATE resource_leases SET process_id=NULL WHERE task_id='child'`)
			}
			if mode == "capacity" {
				_, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<65) INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT token||'-'||n.x,task_id,owner||'-'||n.x,scope,writer,expires,released,process_id FROM resource_leases,n WHERE task_id='child'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
				t.Fatal("unsafe reader admitted", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanPendingToolsAtomicSuffixAndReaderDrift(t *testing.T) {
	for _, boundary := range []string{"tool", "terminal", "head", "receipt", "release", "reader-drift"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "readtool_pending_reader")
			kill()
			before := orphanHistories(t, s)
			trigger := `BEFORE INSERT ON events WHEN NEW.task_id='child' AND NEW.sequence=5 BEGIN SELECT RAISE(ABORT,'fixture'); END`
			switch boundary {
			case "terminal":
				trigger = `BEFORE INSERT ON events WHEN NEW.task_id='child' AND NEW.sequence=6 BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "head":
				trigger = `BEFORE UPDATE OF sequence ON task_heads WHEN NEW.task_id='child' BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "receipt":
				trigger = `BEFORE INSERT ON lease_recoveries BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "release":
				trigger = `BEFORE UPDATE OF released ON resource_leases BEGIN SELECT RAISE(ABORT,'fixture'); END`
			case "reader-drift":
				trigger = `BEFORE UPDATE OF released ON resource_leases WHEN NEW.task_id='work' BEGIN UPDATE resource_leases SET expires=expires+1 WHERE task_id='child'; END`
			}
			if _, err := s.db.Exec(`CREATE TRIGGER block_pending ` + trigger); err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
				t.Fatal(boundary, changed, err)
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanPendingToolsReceiptRejectsCanonicalSuffixDrift(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "readtool_pending")
	kill()
	ctx := context.Background()
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	history := orphanHistories(t, s)["child"]
	tool := history[len(history)-2]
	tool.Data.Text = `{"error":"different"}`
	body, err := tool.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE events SET body=? WHERE id=?`, body, tool.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
		t.Fatal("altered synthetic result accepted", changed, err)
	}
}

func TestOrphanPendingToolsAdmitsExactReaderBound(t *testing.T) {
	s, _, token, kill := startOrphanWorkerOwner(t, "readtool_pending_reader")
	kill()
	ctx := context.Background()
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<63) INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT token||'-'||n.x,task_id,owner||'-'||n.x,scope,writer,expires,released,process_id FROM resource_leases,n WHERE task_id='child'`); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
		t.Fatal("exact reader bound rejected", changed, err)
	}
	var held int
	if err := s.db.QueryRow(`SELECT count(*) FROM resource_leases WHERE task_id='child' AND released=0`).Scan(&held); err != nil || held != 64 {
		t.Fatal("child readers released during worker transaction", held, err)
	}
}
