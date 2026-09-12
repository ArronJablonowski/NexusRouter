package telemetry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func submittedEvent(id, task, session string, sequence int64, kind runtime.Kind, submission string) runtime.Event {
	e := runtime.Event{Version: 1, ID: id, TaskID: task, SessionID: session, CorrelationID: task, Sequence: sequence, Time: time.Unix(100+sequence, 0).UTC(), Kind: kind}
	if kind == runtime.TaskStarted {
		e.Data.SubmissionID = submission
	}
	return e
}

func TestSubmissionStreamPagePreservesGlobalCommitOrderAndTerminalMarker(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "stream-key")
	claim := claimSubmission(t, db)
	oneStart := submittedEvent("one-start", "one", "one-session", 1, runtime.TaskStarted, claim.Status.ID)
	twoStart := submittedEvent("two-start", "two", "two-session", 1, runtime.TaskStarted, claim.Status.ID)
	oneDone := submittedEvent("one-done", "one", "one-session", 2, runtime.TaskFailed, "")
	twoDone := submittedEvent("two-done", "two", "two-session", 2, runtime.TaskCompleted, "")
	for _, appendEvent := range []struct {
		expected int64
		event    runtime.Event
	}{{0, oneStart}, {0, twoStart}, {1, oneDone}, {1, twoDone}} {
		if err := db.AppendSubmission(ctx, appendEvent.expected, appendEvent.event, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendSubmission(ctx, 1, twoDone, claim.Status.ID, claim.Token); err != nil {
		t.Fatal("exact retry", err)
	}
	var mapped int
	if err := db.db.QueryRow(`SELECT count(*) FROM submission_stream_events WHERE submission_id=?`, claim.Status.ID).Scan(&mapped); err != nil || mapped != 4 {
		t.Fatal("exact retry duplicated stream order", mapped, err)
	}
	result := &submissions.Result{TaskID: "two", PreviousTaskIDs: []string{"one"}, Text: "answer", Turns: 1, FinishReason: "stop"}
	if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", result); err != nil {
		t.Fatal(err)
	}
	page, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100)
	if err != nil || page.EventHeadSequence != 4 || page.ResultSequence != 5 || page.NextSequence != 4 || page.HasMoreEvents || page.Status.State != "succeeded" {
		t.Fatal(page, err)
	}
	got := make([]string, len(page.Events))
	for i, item := range page.Events {
		got[i] = item.Event.ID
		if item.Sequence != int64(i+1) {
			t.Fatal(item)
		}
	}
	if want := []string{"one-start", "two-start", "one-done", "two-done"}; !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	resumed, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 2, 1)
	if err != nil || len(resumed.Events) != 1 || resumed.Events[0].Sequence != 3 || resumed.Events[0].Event.ID != "one-done" || !resumed.HasMoreEvents {
		t.Fatal(resumed, err)
	}
	marker, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 5, 100)
	if err != nil || len(marker.Events) != 0 || marker.NextSequence != 5 || marker.ResultSequence != 5 {
		t.Fatal(marker, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	afterRestart, err := reopened.ReadSubmissionStreamPage(ctx, claim.Status.ID, 4, 100)
	if err != nil || afterRestart.ResultSequence != 5 || afterRestart.Status.Result == nil || afterRestart.Status.Result.Text != "answer" {
		t.Fatal(afterRestart, err)
	}
}

func TestSubmissionStreamReaderRejectsForeignMappedTask(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "owned-stream")
	owned := claimSubmission(t, db)
	ownedStart := submittedEvent("owned-start", "owned-task", "owned-session", 1, runtime.TaskStarted, owned.Status.ID)
	ownedDone := submittedEvent("owned-done", "owned-task", "owned-session", 2, runtime.TaskFailed, "")
	if db.AppendSubmission(ctx, 0, ownedStart, owned.Status.ID, owned.Token) != nil || db.AppendSubmission(ctx, 1, ownedDone, owned.Status.ID, owned.Token) != nil {
		t.Fatal("owned fixture")
	}

	queuedSubmission(t, db, "foreign-stream")
	foreign := claimSubmission(t, db)
	foreignStart := submittedEvent("foreign-start", "foreign-task", "foreign-session", 1, runtime.TaskStarted, foreign.Status.ID)
	foreignDone := submittedEvent("foreign-done", "foreign-task", "foreign-session", 2, runtime.TaskFailed, "")
	if db.AppendSubmission(ctx, 0, foreignStart, foreign.Status.ID, foreign.Token) != nil || db.AppendSubmission(ctx, 1, foreignDone, foreign.Status.ID, foreign.Token) != nil {
		t.Fatal("foreign fixture")
	}
	if _, err := db.db.Exec(`DELETE FROM submission_stream_events WHERE event_id=?`, foreignDone.ID); err != nil {
		t.Fatal(err)
	}
	var foreignBody []byte
	if err := db.db.QueryRow(`SELECT body FROM events WHERE id=?`, foreignDone.ID).Scan(&foreignBody); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE submission_stream_events SET event_id=?,task_id=?,task_sequence=?,body_digest=? WHERE submission_id=? AND sequence=2`, foreignDone.ID, foreignDone.TaskID, foreignDone.Sequence, streamBodyDigest(foreignBody), owned.Status.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadSubmissionStreamPage(ctx, owned.Status.ID, 0, 100); !errors.Is(err, sessions.ErrEventPage) {
		t.Fatal("foreign task mapping accepted", err)
	}
}

func TestSubmissionStreamReaderRequiresExactMappedTaskSet(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "task-set-stream")
	claim := claimSubmission(t, db)
	events := []runtime.Event{
		submittedEvent("set-one-start", "set-one", "set-one-session", 1, runtime.TaskStarted, claim.Status.ID),
		submittedEvent("set-two-start", "set-two", "set-two-session", 1, runtime.TaskStarted, claim.Status.ID),
		submittedEvent("set-one-done", "set-one", "set-one-session", 2, runtime.TaskFailed, ""),
		submittedEvent("set-two-done", "set-two", "set-two-session", 2, runtime.TaskFailed, ""),
	}
	for _, event := range events {
		if err := db.AppendSubmission(ctx, event.Sequence-1, event, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`UPDATE submission_stream_events SET task_id='set-one' WHERE submission_id=? AND task_id='set-two'`, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100); !errors.Is(err, sessions.ErrEventPage) {
		t.Fatal("incomplete mapped task set accepted", err)
	}
}

func TestSubmissionStreamTerminalMarkerRequiresTerminalTaskCoverage(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "terminal-coverage")
	claim := claimSubmission(t, db)
	start := submittedEvent("terminal-running-start", "terminal-running", "terminal-session", 1, runtime.TaskStarted, claim.Status.ID)
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE submissions SET state='failed',error_code='execution_failed',result=NULL WHERE id=?`, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100); !errors.Is(err, sessions.ErrEventPage) {
		t.Fatal("terminal marker overtook running task", err)
	}
}

func TestSubmissionStreamAllowsDurableFailureWithoutTask(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "terminal-without-task")
	claim := claimSubmission(t, db)
	if _, err := db.db.Exec(`UPDATE submissions SET state='failed',error_code='recovery_exhausted',result=NULL WHERE id=?`, claim.Status.ID); err != nil {
		t.Fatal(err)
	}
	page, err := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100)
	if err != nil || page.EventHeadSequence != 0 || page.ResultSequence != 1 || len(page.Events) != 0 {
		t.Fatal(page, err)
	}
}

func TestSubmissionStreamMappingAppendIsAtomic(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "atomic-stream")
	claim := claimSubmission(t, db)
	if _, err := db.db.Exec(`CREATE TRIGGER reject_stream_mapping BEFORE INSERT ON submission_stream_events BEGIN SELECT RAISE(ABORT,'reject'); END;`); err != nil {
		t.Fatal(err)
	}
	start := submittedEvent("atomic-start", "atomic", "atomic-session", 1, runtime.TaskStarted, claim.Status.ID)
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err == nil {
		t.Fatal("mapping failure committed event")
	}
	var events, heads int
	if db.db.QueryRow(`SELECT count(*) FROM events WHERE task_id='atomic'`).Scan(&events) != nil || db.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id='atomic'`).Scan(&heads) != nil || events != 0 || heads != 0 {
		t.Fatal("append rollback incomplete", events, heads)
	}
	if _, err := db.db.Exec(`DROP TRIGGER reject_stream_mapping`); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionStreamMigrationBackfillsStableOrder(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "migration-stream")
	claim := claimSubmission(t, db)
	oneStart := submittedEvent("migration-one-start", "migration-one", "migration-one-session", 1, runtime.TaskStarted, claim.Status.ID)
	twoStart := submittedEvent("migration-two-start", "migration-two", "migration-two-session", 1, runtime.TaskStarted, claim.Status.ID)
	oneDone := submittedEvent("migration-one-done", "migration-one", "migration-one-session", 2, runtime.TaskFailed, "")
	twoDone := submittedEvent("migration-two-done", "migration-two", "migration-two-session", 2, runtime.TaskCompleted, "")
	for _, item := range []struct {
		expected int64
		event    runtime.Event
	}{{0, oneStart}, {0, twoStart}, {1, oneDone}, {1, twoDone}} {
		if db.AppendSubmission(ctx, item.expected, item.event, claim.Status.ID, claim.Token) != nil {
			t.Fatal("fixture append")
		}
	}
	if _, err := db.FinishSubmission(ctx, claim.Status.ID, claim.Token, "succeeded", "", &submissions.Result{TaskID: twoDone.TaskID, PreviousTaskIDs: []string{oneDone.TaskID}, Text: "answer", Turns: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE submission_stream_events; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=31`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	page, err := migrated.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100)
	var schema int
	want := []string{oneStart.ID, twoStart.ID, oneDone.ID, twoDone.ID}
	got := make([]string, len(page.Events))
	for i := range page.Events {
		got[i] = page.Events[i].Event.ID
	}
	if migrated.db.QueryRow(`PRAGMA user_version`).Scan(&schema) != nil || schema != currentStorageSchema || err != nil || !reflect.DeepEqual(got, want) || page.ResultSequence != 5 {
		t.Fatal(schema, page, err)
	}
}

func TestSubmissionStreamMigrationRejectsCorruptHistoryAtomically(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	queuedSubmission(t, db, "migration-corrupt")
	claim := claimSubmission(t, db)
	start := submittedEvent("migration-corrupt-start", "migration-corrupt-task", "migration-corrupt-session", 1, runtime.TaskStarted, claim.Status.ID)
	if err := db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DROP TABLE submission_stream_events; UPDATE task_heads SET session_id='different-session' WHERE task_id=?; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_settlement_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_settlements_board; DROP TABLE IF EXISTS workboard_auxiliary_review_settlements; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_auxiliary_review_admission_binding; DROP INDEX IF EXISTS workboard_auxiliary_review_admissions_board; DROP TABLE IF EXISTS workboard_auxiliary_review_admissions; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_settlement_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_settlement_binding; DROP INDEX IF EXISTS workboard_execution_settlements_board; DROP TABLE IF EXISTS workboard_execution_settlements; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_delete; DROP TRIGGER IF EXISTS workboard_execution_admission_immutable_update; DROP TRIGGER IF EXISTS workboard_execution_admission_binding; DROP TRIGGER IF EXISTS workboard_execution_admission_no_active; DROP INDEX IF EXISTS workboard_execution_admissions_card; DROP INDEX IF EXISTS workboard_execution_admissions_board; DROP INDEX IF EXISTS workboard_execution_admissions_global; DROP TABLE IF EXISTS workboard_execution_admissions; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=31`, start.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if migrated, err := Open(ctx, path); err == nil {
		migrated.Close()
		t.Fatal("corrupt schema-31 history migrated")
	}
	unchanged, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer unchanged.Close()
	var schema int
	var table bool
	if err := unchanged.db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := unchanged.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='submission_stream_events')`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if schema != 31 || table {
		t.Fatal("failed migration changed durable schema", schema, table)
	}
}

func TestSubmissionByKeyIsNonCreatingAndExact(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	created := queuedSubmission(t, db, "exact-key")
	got, err := db.SubmissionByKey(ctx, submitDigest("exact-key"), submitDigest(`{"prompt":"private-request"}`), submitDigest("config"))
	if err != nil || got.ID != created.ID {
		t.Fatal(got, err)
	}
	if _, err := db.SubmissionByKey(ctx, submitDigest("exact-key"), submitDigest(`{}`), submitDigest("config")); err != submissions.ErrConflict {
		t.Fatal(err)
	}
	if _, err := db.SubmissionByKey(ctx, submitDigest("missing-key"), submitDigest(`{}`), submitDigest("config")); err == nil {
		t.Fatal("missing key created a submission")
	}
}

func TestSubmissionStreamReaderRejectsCorruptionBeforePayload(t *testing.T) {
	for _, mode := range []string{"digest", "mapping", "gap", "order", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			queuedSubmission(t, db, "corrupt-"+mode)
			claim := claimSubmission(t, db)
			start := submittedEvent("corrupt-start-"+mode, "corrupt-"+mode, "corrupt-session", 1, runtime.TaskStarted, claim.Status.ID)
			done := submittedEvent("corrupt-done-"+mode, "corrupt-"+mode, "corrupt-session", 2, runtime.TaskCompleted, "")
			if db.AppendSubmission(ctx, 0, start, claim.Status.ID, claim.Token) != nil || db.AppendSubmission(ctx, 1, done, claim.Status.ID, claim.Token) != nil {
				t.Fatal("fixture")
			}
			var err error
			switch mode {
			case "digest":
				_, err = db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.text','changed') WHERE id=?`, done.ID)
			case "mapping":
				_, err = db.db.Exec(`DELETE FROM submission_stream_events WHERE event_id=?`, done.ID)
			case "gap":
				_, err = db.db.Exec(`UPDATE task_heads SET sequence=3 WHERE task_id=?`, done.TaskID)
			case "order":
				if _, err = db.db.Exec(`UPDATE submission_stream_events SET sequence=100 WHERE event_id=?`, start.ID); err == nil {
					_, err = db.db.Exec(`UPDATE submission_stream_events SET sequence=1 WHERE event_id=?`, done.ID)
				}
				if err == nil {
					_, err = db.db.Exec(`UPDATE submission_stream_events SET sequence=2 WHERE event_id=?`, start.ID)
				}
			case "oversize":
				_, err = db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.text',?) WHERE id=?`, strings.Repeat("x", sessions.MaxEventPageBytes+1), done.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, readErr := db.ReadSubmissionStreamPage(ctx, claim.Status.ID, 0, 100)
			err = readErr
			if mode == "oversize" {
				if err == nil && len(page.Events) == 1 {
					_, err = db.ReadSubmissionStreamPage(ctx, claim.Status.ID, page.NextSequence, 100)
				}
				if !errors.Is(err, sessions.ErrEventTooLarge) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, sessions.ErrEventPage) {
				t.Fatal(err)
			}
		})
	}
}
