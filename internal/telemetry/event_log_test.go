package telemetry

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func logEvent(id, task, session string, sequence int64, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: id, TaskID: task, SessionID: session, CorrelationID: task, Sequence: sequence, Time: time.Unix(100+sequence, 0).UTC(), Kind: kind}
}

func TestCommittedEventLogFrozenHighWaterAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 0, logEvent("a-start", "a", "a-session", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, logEvent("a-done", "a", "a-session", 2, runtime.TaskCompleted)); err != nil {
		t.Fatal(err)
	}
	first, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 1})
	if err != nil || first.Validate() != nil || len(first.Events) != 1 || first.Events[0].Position != 1 || !first.HasMore {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err = db.Append(ctx, 0, logEvent("b-start", "b", "b-session", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	second, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{After: first.NextCursor, Limit: 100})
	if err != nil || second.Validate() != nil || len(second.Events) != 1 || second.Events[0].Event.ID != "a-done" || second.HasMore {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	third, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{After: second.NextCursor, Limit: 100})
	if err != nil || third.Validate() != nil || len(third.Events) != 1 || third.Events[0].Event.ID != "b-start" || third.HasMore {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	empty, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{After: third.NextCursor, Limit: 100})
	if err != nil || empty.Validate() != nil || len(empty.Events) != 0 || empty.NextCursor == "" || empty.HasMore {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

func TestCommittedEventLogEmptyCursorAndCancellation(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ReadCommittedEventPage(context.Background(), sessions.EventLogOptions{Limit: 10})
	if err != nil || page.Validate() != nil || page.NextCursor == "" || len(page.Events) != 0 {
		t.Fatal(page, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err = db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 10})
	if !errors.Is(err, context.Canceled) || page.Version != 0 || len(page.Events) != 0 {
		t.Fatal(page, err)
	}
}

func TestCommittedEventLogRejectsForeignAndCorruptAnchors(t *testing.T) {
	ctx := context.Background()
	first, err := Open(ctx, filepath.Join(t.TempDir(), "first.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = first.Append(ctx, 0, logEvent("first", "first-task", "first-session", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	page, err := first.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, filepath.Join(t.TempDir(), "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = second.Append(ctx, 0, logEvent("second", "second-task", "second-session", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	if got, err := second.ReadCommittedEventPage(ctx, sessions.EventLogOptions{After: page.NextCursor, Limit: 10}); !errors.Is(err, sessions.ErrEventLog) || len(got.Events) != 0 {
		t.Fatal("foreign cursor accepted", got, err)
	}
	cursor, _ := sessions.ParseEventLogCursor(page.NextCursor)
	cursor.LastEventID = "wrong"
	cursor.HighWaterEventID = "wrong"
	changed, _ := sessions.EncodeEventLogCursor(cursor)
	if got, err := first.ReadCommittedEventPage(ctx, sessions.EventLogOptions{After: changed, Limit: 10}); !errors.Is(err, sessions.ErrEventLog) || len(got.Events) != 0 {
		t.Fatal("corrupt anchor accepted", got, err)
	}
}

func TestCommittedEventLogRejectsMissingMiddleTailAndBodyCorruption(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"missing middle index", `DELETE FROM event_log WHERE position=2`},
		{"missing tail index", `DELETE FROM event_log WHERE position=3`},
		{"missing event and index", `DELETE FROM event_log WHERE position=2; DELETE FROM events WHERE sequence=2`},
		{"position gap", `UPDATE event_log SET position=position+10`},
		{"task position inversion", `UPDATE event_log SET position=100 WHERE position=1; UPDATE event_log SET position=1 WHERE position=2; UPDATE event_log SET position=2 WHERE position=100`},
		{"wrong mapping", `UPDATE event_log SET task_sequence=1 WHERE position=2`},
		{"wrong digest", `UPDATE event_log SET body_digest=lower(hex(randomblob(32))) WHERE position=2`},
		{"changed body", `UPDATE events SET body=json_set(body,'$.data.text','changed') WHERE sequence=2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TaskCompleted} {
				e := logEvent(string(kind), "task", "session", int64(i+1), kind)
				e.TurnID = "turn"
				if err = db.Append(ctx, int64(i), e); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			page, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 100})
			if !errors.Is(err, sessions.ErrEventLog) || len(page.Events) != 0 {
				t.Fatal("corruption accepted", page, err)
			}
		})
	}
}

func TestCommittedEventLogExactRetryAndAtomicIndexFailure(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := logEvent("start", "task", "session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 0, start); err != nil {
		t.Fatal("exact retry", err)
	}
	var count int
	if err = db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = db.db.Exec(`CREATE TRIGGER reject_event_log BEFORE INSERT ON event_log BEGIN SELECT RAISE(ABORT,'reject'); END`); err != nil {
		t.Fatal(err)
	}
	other := logEvent("other", "other-task", "other-session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, other); err == nil {
		t.Fatal("index failure accepted")
	}
	var events, heads int
	if db.db.QueryRow(`SELECT count(*) FROM events WHERE task_id='other-task'`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id='other-task'`).Scan(&heads) != nil || events != 0 || heads != 0 {
		t.Fatal("partial event commit", events, heads)
	}
}

func TestCommittedEventLogMigrationBackfillsCanonicalHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TaskCompleted} {
		if err = db.Append(ctx, int64(i), logEvent(string(kind), "task", "session", int64(i+1), kind)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.db.Exec(`DROP TABLE event_log; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 100})
	if err != nil || len(page.Events) != 2 || page.Events[0].Position != 1 || page.Events[1].Position != 2 {
		t.Fatal(page, err)
	}
	var schema int
	if db.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || schema != currentStorageSchema {
		t.Fatal(schema)
	}
}

func TestCommittedEventLogMigrationPreservesLegacyColonEventID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := logEvent("legacy:provider:event", "task", "session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`DROP TABLE event_log; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 10})
	if err != nil || page.Validate() != nil || len(page.Events) != 1 || page.Events[0].Event.ID != legacy.ID {
		t.Fatal(page, err)
	}
	cursor, err := sessions.ParseEventLogCursor(page.NextCursor)
	if err != nil || cursor.LastEventID != legacy.ID || cursor.HighWaterEventID != legacy.ID {
		t.Fatal(cursor, err)
	}
}

func TestCommittedEventLogMigrationRejectsLegacyTaskRowIDInversion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	start := logEvent("start", "task", "session", 1, runtime.TaskStarted)
	done := logEvent("done", "task", "session", 2, runtime.TaskCompleted)
	if err = db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, done); err != nil {
		t.Fatal(err)
	}
	startBody, _ := start.Encode()
	doneBody, _ := done.Encode()
	if _, err = db.db.Exec(`DROP TABLE event_log; DELETE FROM events; INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,2,?),(?,?,1,?); DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`, done.ID, done.TaskID, doneBody, start.ID, start.TaskID, startBody); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("legacy per-task rowid inversion accepted")
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var schema int
	if ro.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || schema != 32 {
		t.Fatal("failed migration advanced schema", schema)
	}
}

func TestCommittedEventLogMigrationRejectsOrphanHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 0, logEvent("start", "task", "session", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`DROP TABLE event_log; PRAGMA foreign_keys=OFF; INSERT INTO events(id,task_id,sequence,body) VALUES('orphan','missing',1,X'7b7d'); DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("orphan migration accepted")
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var schema int
	if ro.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || schema != 32 {
		t.Fatal("failed migration advanced schema", schema)
	}
}

func TestCommittedEventLogRejectsRawOversizedEventBeforeCommit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := logEvent("large", "large-task", "large-session", 1, runtime.TaskStarted)
	e.Data.Text = strings.Repeat("x", sessions.MaxCommittedEventPageBytes+1)
	if err = db.Append(ctx, 0, e); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("raw oversized event committed", err)
	}
	var events int
	if db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events) != nil || events != 0 {
		t.Fatal("raw oversized event left state", events)
	}
}

func wrapperOverflowLogEvent(t *testing.T, id, task string) (runtime.Event, []byte) {
	t.Helper()
	event := logEvent(id, task, task+"-session", 1, runtime.TaskStarted)
	event.Data.Text = strings.Repeat("x", sessions.MaxCommittedEventPageBytes)
	body, err := event.Encode()
	if err != nil {
		t.Fatal(err)
	}
	trim := len(body) - (sessions.MaxCommittedEventPageBytes - 1)
	if trim <= 0 || trim >= len(event.Data.Text) {
		t.Fatal("could not construct wrapper boundary")
	}
	event.Data.Text = event.Data.Text[:len(event.Data.Text)-trim]
	body, err = event.Encode()
	if err != nil || len(body) != sessions.MaxCommittedEventPageBytes-1 {
		t.Fatal("wrong boundary body", len(body), err)
	}
	return event, body
}

func TestCommittedEventLogRejectsWrapperOverflowBeforeCommit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	poison, _ := wrapperOverflowLogEvent(t, "wrapper-poison", "poison-task")
	if err = db.Append(ctx, 0, poison); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("wrapper poison committed", err)
	}
	var heads, events, ledger int
	if db.db.QueryRow(`SELECT count(*) FROM task_heads`).Scan(&heads) != nil || db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || heads != 0 || events != 0 || ledger != 0 {
		t.Fatal("rejected append left state", heads, events, ledger)
	}
	valid := logEvent("valid", "valid-task", "valid-session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, valid); err != nil {
		t.Fatal("later event blocked", err)
	}
}

func TestCommittedEventLogRejectsUnpageableEnvelopeIDsBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*runtime.Event)
	}{
		{"event", func(event *runtime.Event) { event.ID = strings.Repeat("x", 129) }},
		{"task", func(event *runtime.Event) { event.TaskID, event.CorrelationID = "task:invalid", "task:invalid" }},
		{"session", func(event *runtime.Event) { event.SessionID = "session:invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			event := logEvent("event", "task", "session", 1, runtime.TaskStarted)
			tc.mutate(&event)
			if err := db.Append(context.Background(), 0, event); !errors.Is(err, sessions.ErrEventLog) {
				t.Fatal("unpageable identity committed", err)
			}
			var heads, events, ledger int
			if db.db.QueryRow(`SELECT count(*) FROM task_heads`).Scan(&heads) != nil || db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || heads != 0 || events != 0 || ledger != 0 {
				t.Fatal("rejected identity left state", heads, events, ledger)
			}
		})
	}
}

func TestCommittedEventLogDirectWrapperPoisonFailsReadWithoutPartial(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	poison, body := wrapperOverflowLogEvent(t, "direct-poison", "direct-task")
	if _, err = db.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?,?,1,'running')`, poison.TaskID, poison.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,1,?)`, poison.ID, poison.TaskID, body); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`INSERT INTO event_log(event_id,task_id,task_sequence,body_digest) VALUES(?,?,1,?)`, poison.ID, poison.TaskID, streamBodyDigest(body)); err != nil {
		t.Fatal(err)
	}
	page, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 1})
	if !errors.Is(err, sessions.ErrEventTooLarge) || page.Version != 0 || len(page.Events) != 0 || page.NextCursor != "" {
		t.Fatal("wrapper poison leaked partial page", page, err)
	}
}

func TestCommittedEventLogMigrationWrapperOverflowRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	poison, body := wrapperOverflowLogEvent(t, "legacy-poison", "legacy-task")
	if _, err = db.db.Exec(`DROP TABLE event_log; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`INSERT INTO task_heads(task_id,session_id,sequence,state) VALUES(?,?,1,'running')`, poison.TaskID, poison.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`INSERT INTO events(id,task_id,sequence,body) VALUES(?,?,1,?)`, poison.ID, poison.TaskID, body); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); !errors.Is(err, sessions.ErrEventTooLarge) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatal("legacy wrapper poison migrated", err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var schema, ledger int
	if ro.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || ro.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='event_log'`).Scan(&ledger) != nil || schema != 32 || ledger != 0 {
		t.Fatal("failed migration did not roll back", schema, ledger)
	}
}

func TestCommittedEventLogInterruptedRecoveryBatchAndRollback(t *testing.T) {
	for _, mode := range []string{"model", "delegation"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			job := queuedSubmission(t, db, "event-log-"+mode)
			claim := claimSubmission(t, db)
			if mode == "model" {
				interruptedModelFixture(t, db, claim, "")
			} else {
				interruptedTree(t, db, claim)
			}
			var beforeEvents, beforeLog, beforeReceipts int
			if db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&beforeEvents) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&beforeLog) != nil || db.db.QueryRow(`SELECT count(*) FROM submission_recoveries`).Scan(&beforeReceipts) != nil {
				t.Fatal("fixture counts")
			}
			if _, err := db.db.Exec(`CREATE TRIGGER reject_recovery_log BEFORE INSERT ON event_log BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
				t.Fatal(err)
			}
			recover := db.RecoverInterruptedModelCommit
			if mode == "delegation" {
				recover = db.RecoverInterruptedDelegationCommit
			}
			commit, err := recover(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if err == nil || commit.Changed || len(commit.Events) != 0 {
				t.Fatal("ledger failure escaped", commit, err)
			}
			var events, ledger, receipts int
			if db.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || db.db.QueryRow(`SELECT count(*) FROM submission_recoveries`).Scan(&receipts) != nil || events != beforeEvents || ledger != beforeLog || receipts != beforeReceipts {
				t.Fatal("partial recovery", events, ledger, receipts)
			}
			if _, err = db.db.Exec(`DROP TRIGGER reject_recovery_log`); err != nil {
				t.Fatal(err)
			}
			commit, err = recover(context.Background(), job.ID, submitDigest("config"), time.Now().Add(3*time.Minute))
			want := 1
			if mode == "delegation" {
				want = 2
			}
			if err != nil || !commit.Changed || len(commit.Events) != want {
				t.Fatal(commit, err)
			}
			positions := make([]int64, want)
			for i, event := range commit.Events {
				if err := db.db.QueryRow(`SELECT position FROM event_log WHERE event_id=?`, event.ID).Scan(&positions[i]); err != nil || i > 0 && positions[i] != positions[i-1]+1 {
					t.Fatal("non-adjacent recovery batch", positions, err)
				}
			}
			if repeat, err := recover(context.Background(), job.ID, submitDigest("config"), time.Now().Add(4*time.Minute)); err != nil || repeat.Changed || len(repeat.Events) != 0 {
				t.Fatal("recovery repeated", repeat, err)
			}
			if db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || ledger != beforeLog+want {
				t.Fatal("duplicate ledger rows", ledger)
			}
		})
	}
}

func TestCommittedEventLogMigrationPreservesInterleavedGlobalOrderAndContinues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := []runtime.Event{
		logEvent("a-start", "a", "a-session", 1, runtime.TaskStarted),
		logEvent("b-start", "b", "b-session", 1, runtime.TaskStarted),
		logEvent("a-done", "a", "a-session", 2, runtime.TaskCompleted),
		logEvent("b-done", "b", "b-session", 2, runtime.TaskCompleted),
	}
	expected := map[string]int64{}
	for _, event := range want {
		if err = db.Append(ctx, expected[event.TaskID], event); err != nil {
			t.Fatal(err)
		}
		expected[event.TaskID]++
	}
	if _, err = db.db.Exec(`DROP TABLE event_log; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ReadCommittedEventPage(ctx, sessions.EventLogOptions{Limit: 100})
	if err != nil || len(page.Events) != len(want) {
		t.Fatal(page, err)
	}
	for i, event := range want {
		if page.Events[i].Position != int64(i+1) || page.Events[i].Event.ID != event.ID {
			t.Fatal("migration changed interleaved order", page.Events)
		}
	}
	next := logEvent("c-start", "c", "c-session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, next); err != nil {
		t.Fatal(err)
	}
	var position int64
	if err = db.db.QueryRow(`SELECT position FROM event_log WHERE event_id=?`, next.ID).Scan(&position); err != nil || position != int64(len(want)+1) {
		t.Fatal("post-migration sequence did not continue", position, err)
	}
}

func TestCommittedEventLogAutoincrementExhaustionRollsBackAppend(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prior := logEvent("prior", "prior-task", "prior-session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, prior); err != nil {
		t.Fatal(err)
	}
	type ledgerRow struct {
		position, sequence      int64
		eventID, taskID, digest string
	}
	var before, after ledgerRow
	if err = db.db.QueryRow(`SELECT position,event_id,task_id,task_sequence,body_digest FROM event_log WHERE event_id=?`, prior.ID).Scan(&before.position, &before.eventID, &before.taskID, &before.sequence, &before.digest); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='event_log'`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	next := logEvent("next", "next-task", "next-session", 1, runtime.TaskStarted)
	if err = db.Append(ctx, 0, next); err == nil {
		t.Fatal("exhausted ledger accepted append")
	}
	var heads, events, ledger int
	if db.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, next.TaskID).Scan(&heads) != nil || db.db.QueryRow(`SELECT count(*) FROM events WHERE task_id=?`, next.TaskID).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM event_log`).Scan(&ledger) != nil || heads != 0 || events != 0 || ledger != 1 {
		t.Fatal("exhausted append partially committed", heads, events, ledger)
	}
	if err = db.db.QueryRow(`SELECT position,event_id,task_id,task_sequence,body_digest FROM event_log WHERE event_id=?`, prior.ID).Scan(&after.position, &after.eventID, &after.taskID, &after.sequence, &after.digest); err != nil || after != before {
		t.Fatal("prior ledger changed", before, after, err)
	}
}
