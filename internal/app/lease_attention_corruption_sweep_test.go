package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func TestDispatcherLeaseAttentionSweepPassesCorruptFirstPage(t *testing.T) {
	svc, db, calls := recoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	at := time.Now().Add(-time.Minute).UTC()
	start := runtime.Event{Version: 1, ID: "attention-corrupt-start", TaskID: "attention-corrupt-task", SessionID: "attention-session", CorrelationID: "attention-corrupt-task", Sequence: 1, Time: at, Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	// An empty private token is malformed but has an expired integer timestamp,
	// so it must be visited, rejected, and skipped without repair. Thirty-three
	// later rows require more than the production 32-candidate page budget.
	for i := 0; i < 34; i++ {
		token := fmt.Sprintf("private-token-%02d", i)
		if i == 0 {
			token = ""
		}
		if _, err := fixture.ExecContext(ctx, `INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released) VALUES(?,?,?,?,1,?,0)`, token, start.TaskID, "private-owner", fmt.Sprintf("private-scope-%02d", i), at.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	type leaseRow struct {
		Row                       int64
		Token, Task, Owner, Scope string
		Writer, Expires, Released int64
		Process                   sql.NullString
	}
	readLeases := func() []leaseRow {
		t.Helper()
		rows, err := fixture.QueryContext(ctx, `SELECT rowid,token,task_id,owner,scope,writer,expires,released,process_id FROM resource_leases ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []leaseRow
		for rows.Next() {
			var r leaseRow
			if err := rows.Scan(&r.Row, &r.Token, &r.Task, &r.Owner, &r.Scope, &r.Writer, &r.Expires, &r.Released, &r.Process); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := readLeases()
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "") // Metadata sweep must not acquire/probe a guard.
	dispatcher, err := StartDispatcher(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var page workers.LeaseAttentionPage
	for {
		page, err = db.ListLeaseAttention(ctx, workers.LeaseAttentionOptions{State: "open", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 33 && dispatcher.Health().Status == "degraded" && dispatcher.Health().Code == "supervisor_error" {
			break
		}
		select {
		case <-ticker.C:
		case <-dispatcher.done:
			t.Fatal("dispatcher stopped before later page")
		case <-ctx.Done():
			t.Fatal("corrupt first row pinned later attention pages", len(page.Items), dispatcher.Health())
		}
	}
	if err := dispatcher.Close(); !errors.Is(err, ErrSubmission) {
		t.Fatal("corrupt candidate not sticky diagnostic", err)
	}
	if page.Validate() != nil {
		t.Fatal("invalid attention page")
	}
	var lastID string
	if err := fixture.QueryRowContext(ctx, `SELECT id FROM lease_attention WHERE lease_token='private-token-33'`).Scan(&lastID); err != nil {
		t.Fatal("last-page candidate not observed", err)
	}
	history, err := db.ListLeaseAttentionHistory(ctx, lastID, workers.LeaseAttentionHistoryOptions{Limit: 25})
	if err != nil || !history.Available || len(history.Items) != 1 || history.Items[0].Kind != "observed" || history.Items[0].Observation.ID != lastID || history.Items[0].Observation.State != "open" {
		t.Fatal("last-page history absent", history, err)
	}
	if !reflect.DeepEqual(before, readLeases()) {
		t.Fatal("sweep changed malformed or valid lease ownership")
	}
	journal, err := db.Read(ctx, start.TaskID, 0, 10)
	if err != nil || !reflect.DeepEqual(journal, []runtime.Event{start}) {
		t.Fatal("sweep changed journal", err)
	}
	var malformed, receipts int
	if err := fixture.QueryRowContext(ctx, `SELECT count(*) FROM lease_attention WHERE lease_token=''`).Scan(&malformed); err != nil || malformed != 0 {
		t.Fatal("malformed candidate repaired", err)
	}
	if err := fixture.QueryRowContext(ctx, `SELECT count(*) FROM lease_recoveries`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("observation recovered lease", err)
	}
	if calls.Load() != 0 {
		t.Fatal("attention sweep executed model")
	}
}
