package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func committedEventFixture(t *testing.T) (string, []runtime.Event) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []runtime.Event{
		{Version: 1, ID: "committed-start", TaskID: "committed-task", SessionID: "committed-session", CorrelationID: "committed-task", Sequence: 1, Time: now, Kind: runtime.TaskStarted},
		{Version: 1, ID: "committed-end", TaskID: "committed-task", SessionID: "committed-session", CorrelationID: "committed-task", Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TaskCompleted, CausationID: "committed-start"},
	}
	for i, event := range events {
		if err := db.Append(ctx, int64(i), event); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, events
}

func TestReadCommittedEventsAdmissionAndMissingDatabase(t *testing.T) {
	valid := sessions.EventLogOptions{Limit: 1}
	for name, call := range map[string]func() error{
		"nil context": func() error { _, err := ReadCommittedEvents(nil, "db", valid); return err },
		"empty path":  func() error { _, err := ReadCommittedEvents(context.Background(), "", valid); return err },
		"zero limit": func() error {
			_, err := ReadCommittedEvents(context.Background(), "db", sessions.EventLogOptions{})
			return err
		},
		"bad cursor": func() error {
			_, err := ReadCommittedEvents(context.Background(), "db", sessions.EventLogOptions{After: "bad", Limit: 1})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrAdmission) {
				t.Fatal(err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	page, err := ReadCommittedEvents(context.Background(), missing, valid)
	if !errors.Is(err, ErrInspection) || !reflect.DeepEqual(page, sessions.CommittedEventPage{}) {
		t.Fatal(page, err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("read-only inspection created missing database", statErr)
	}
}

func TestReadCommittedEventsDetachedFrozenAndCaughtUp(t *testing.T) {
	path, events := committedEventFixture(t)
	ctx := context.Background()
	first, err := ReadCommittedEvents(ctx, path, sessions.EventLogOptions{Limit: 1})
	if err != nil || len(first.Events) != 1 || !first.HasMore || first.NextCursor == "" || !reflect.DeepEqual(first.Events[0].Event, events[0]) {
		t.Fatal(first, err)
	}
	first.Events[0].Event.Data.Code = "caller mutation"
	repeated, err := ReadCommittedEvents(ctx, path, sessions.EventLogOptions{Limit: 1})
	if err != nil || len(repeated.Events) != 1 || repeated.Events[0].Event.Data.Code != "" {
		t.Fatal("returned page aliased durable state", repeated, err)
	}
	last, err := ReadCommittedEvents(ctx, path, sessions.EventLogOptions{After: first.NextCursor, Limit: 1})
	if err != nil || len(last.Events) != 1 || last.HasMore || last.NextCursor == "" || !reflect.DeepEqual(last.Events[0].Event, events[1]) {
		t.Fatal(last, err)
	}
	caughtUp, err := ReadCommittedEvents(ctx, path, sessions.EventLogOptions{After: last.NextCursor, Limit: 1})
	if err != nil || len(caughtUp.Events) != 0 || caughtUp.HasMore || caughtUp.NextCursor != last.NextCursor {
		t.Fatal(caughtUp, err)
	}
}

func TestReadCommittedEventsCancellationAndSchemaFailureAreSanitized(t *testing.T) {
	path, _ := committedEventFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := ReadCommittedEvents(canceled, path, sessions.EventLogOptions{Limit: 1})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(page, sessions.CommittedEventPage{}) {
		t.Fatal(page, err)
	}

	legacy := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_delete; DROP TRIGGER IF EXISTS workboard_task_start_claim_immutable_update; DROP INDEX IF EXISTS workboard_task_start_claims_board; DROP TABLE IF EXISTS workboard_task_start_claims; PRAGMA user_version=32; CREATE TABLE event_log(position INTEGER PRIMARY KEY,event_id TEXT)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	page, err = ReadCommittedEvents(context.Background(), legacy, sessions.EventLogOptions{Limit: 1})
	if !errors.Is(err, sessions.ErrEventLog) || err.Error() != sessions.ErrEventLog.Error() || !reflect.DeepEqual(page, sessions.CommittedEventPage{}) {
		t.Fatal("legacy/counterfeit schema leaked an internal error or page", page, err)
	}
}

func TestReadCommittedEventsCorruptionReturnsOnlyPublicErrorAndNoPartialPage(t *testing.T) {
	path, _ := committedEventFixture(t)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE events SET body='{"private":"corruption marker"}' WHERE id='committed-end'`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadCommittedEvents(context.Background(), path, sessions.EventLogOptions{Limit: 100})
	if !errors.Is(err, sessions.ErrEventLog) || !reflect.DeepEqual(page, sessions.CommittedEventPage{}) || err.Error() != sessions.ErrEventLog.Error() {
		t.Fatal("corruption returned partial data or a private error", page, err)
	}
}
