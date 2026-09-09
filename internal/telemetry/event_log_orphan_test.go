//go:build darwin || linux

package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestCommittedEventLogOrphanChildBatchAndRollback(t *testing.T) {
	db, _, token, kill := startOrphanWorkerOwner(t, "active")
	kill()
	orphanChildStreamPrefix(t, db)
	var beforeEvents, beforeLog, beforeReceipts, beforeReleased int
	if db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&beforeEvents) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&beforeLog) != nil || db.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&beforeReceipts) != nil || db.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&beforeReleased) != nil {
		t.Fatal("fixture counts")
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_orphan_log BEFORE INSERT ON event_log BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if changed, err := db.RecoverOrphanWorker(context.Background(), token, time.Now()); err == nil || changed {
		t.Fatal("ledger failure escaped", changed, err)
	}
	var events, ledger, receipts, released int
	if db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || db.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&receipts) != nil || db.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released) != nil || events != beforeEvents || ledger != beforeLog || receipts != beforeReceipts || released != beforeReleased {
		t.Fatal("partial orphan recovery", events, ledger, receipts, released)
	}
	if _, err := db.db.Exec(`DROP TRIGGER reject_orphan_log`); err != nil {
		t.Fatal(err)
	}
	if changed, err := db.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	rows, err := db.db.Query(`SELECT task_id FROM event_log WHERE position>? ORDER BY position`, beforeLog)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []string
	for rows.Next() {
		var task string
		if err := rows.Scan(&task); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	rows.Close()
	if len(tasks) != 2 || tasks[0] != "child" || tasks[1] != "work" {
		t.Fatal("wrong recovery ledger order", tasks)
	}
	if changed, err := db.RecoverOrphanWorker(context.Background(), token, time.Now()); err != nil || changed {
		t.Fatal("repeat recovery", changed, err)
	}
	if db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || ledger != beforeLog+2 {
		t.Fatal("duplicate orphan ledger rows", ledger)
	}
}
