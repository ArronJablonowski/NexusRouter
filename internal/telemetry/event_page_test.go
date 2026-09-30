package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func eventPageStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "page.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := event(string(kind), int64(i+1), kind)
		e.CorrelationID = "task"
		e.TurnID = "turn"
		e.AttemptID = "attempt"
		if err := db.Append(context.Background(), int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func TestEventPageBoundsMetadataBeforePayloads(t *testing.T) {
	for _, target := range []string{"session", "state", "head_id", "entry_id", "empty_head", "empty_entry"} {
		t.Run(target, func(t *testing.T) {
			db, _ := eventPageStore(t)
			huge := strings.Repeat("private-metadata", 100000)
			var err error
			switch target {
			case "session":
				_, err = db.db.Exec(`UPDATE task_heads SET session_id=?`, huge)
			case "state":
				_, err = db.db.Exec(`UPDATE task_heads SET state=?`, huge)
			case "head_id":
				_, err = db.db.Exec(`UPDATE events SET id=? WHERE sequence=4`, huge)
			case "entry_id":
				_, err = db.db.Exec(`UPDATE events SET id=? WHERE sequence=2`, huge)
			case "empty_head":
				_, err = db.db.Exec(`UPDATE events SET id='' WHERE sequence=4`)
			case "empty_entry":
				_, err = db.db.Exec(`UPDATE events SET id='' WHERE sequence=2`)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := db.ReadEventPage(context.Background(), "task", 0, 100)
			if !errors.Is(err, sessions.ErrEventPage) || len(page.Events) != 0 || strings.Contains(page.SessionID, "private-metadata") || strings.Contains(page.State, "private-metadata") {
				t.Fatal("metadata leak or late validation", page, err)
			}
		})
	}
}

func TestEventPageAllowsOpaqueColonEventIDs(t *testing.T) {
	db, _ := eventPageStore(t)
	if _, err := db.db.Exec(`UPDATE events SET id='event:'||sequence,body=json_set(body,'$.id','event:'||sequence)`); err != nil {
		t.Fatal(err)
	}
	page, err := db.ReadEventPage(context.Background(), "task", 0, 100)
	if err != nil || len(page.Events) != 4 || page.Events[0].ID != "event:1" || page.Events[3].ID != "event:4" {
		t.Fatal(page, err)
	}
}

func TestReadEventPageRestartAndCursor(t *testing.T) {
	db, path := eventPageStore(t)
	ctx := context.Background()
	first, err := db.ReadEventPage(ctx, "task", 0, 2)
	if err != nil || first.Validate() != nil || len(first.Events) != 2 || first.FromSequence != 0 || first.NextSequence != 2 || first.HeadSequence != 4 || !first.HasMore || first.State != "completed" {
		t.Fatalf("page=%+v error=%v", first, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	second, err := ro.ReadEventPage(ctx, "task", first.NextSequence, 100)
	if err != nil || len(second.Events) != 2 || second.HasMore || second.NextSequence != 4 {
		t.Fatalf("page=%+v error=%v", second, err)
	}
	empty, err := ro.ReadEventPage(ctx, "task", 4, 100)
	if err != nil || len(empty.Events) != 0 || empty.HasMore || empty.NextSequence != 4 {
		t.Fatalf("head page=%+v error=%v", empty, err)
	}
	for _, after := range []int64{-1, 5} {
		if _, err := ro.ReadEventPage(ctx, "task", after, 100); !errors.Is(err, sessions.ErrEventCursor) {
			t.Fatal("invalid cursor accepted", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := ro.ReadEventPage(ctx, "task", 0, limit); !errors.Is(err, sessions.ErrEventPage) {
			t.Fatal("invalid limit accepted", err)
		}
	}
	if _, err := ro.ReadEventPage(ctx, "missing", 0, 100); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing task error lost", err)
	}
}

func TestReadEventPageRejectsCorruption(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"gap", "DELETE FROM events WHERE task_id='task' AND sequence=2"},
		{"body sequence", "UPDATE events SET body=json_set(body,'$.sequence',99) WHERE sequence=2"},
		{"body task", "UPDATE events SET body=json_set(body,'$.task_id','other') WHERE sequence=2"},
		{"body session", "UPDATE events SET body=json_set(body,'$.session_id','other') WHERE sequence=2"},
		{"body correlation", "UPDATE events SET body=json_set(body,'$.correlation_id','other') WHERE sequence=2"},
		{"body id", "UPDATE events SET body=json_set(body,'$.id','other') WHERE sequence=2"},
		{"invalid event", "UPDATE events SET body=json_set(body,'$.version',2) WHERE sequence=2"},
		{"head state", "UPDATE task_heads SET state='failed'"},
		{"unknown state", "UPDATE task_heads SET state='unknown'"},
		{"head mismatch", "UPDATE events SET body=json_set(body,'$.sequence',99) WHERE sequence=4"},
		{"missing head", "DELETE FROM events WHERE sequence=4"},
		{"early terminal", "UPDATE events SET body=json_set(body,'$.kind','task.completed') WHERE sequence=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := eventPageStore(t)
			if _, err := db.db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReadEventPage(context.Background(), "task", 0, 100); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
	db, _ := eventPageStore(t)
	if _, err := db.db.Exec("UPDATE task_heads SET state='failed'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadEventPage(context.Background(), "task", 4, 100); err == nil {
		t.Fatal("empty page did not prove terminal head")
	}
}

func TestReadEventPageByteLimit(t *testing.T) {
	db, _ := eventPageStore(t)
	setText := func(seq int64, size int) {
		t.Helper()
		var body []byte
		if err := db.db.QueryRow("SELECT body FROM events WHERE sequence=?", seq).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var e runtime.Event
		if json.Unmarshal(body, &e) != nil {
			t.Fatal("bad fixture")
		}
		e.Data.Text = strings.Repeat("x", size)
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec("UPDATE events SET body=? WHERE sequence=?", body, seq); err != nil {
			t.Fatal(err)
		}
	}
	setText(2, 5<<20)
	setText(3, 5<<20)
	page, err := db.ReadEventPage(context.Background(), "task", 0, 100)
	if err != nil || len(page.Events) != 2 || page.NextSequence != 2 || !page.HasMore {
		t.Fatalf("byte boundary skipped records: %+v %v", page, err)
	}
	next, err := db.ReadEventPage(context.Background(), "task", page.NextSequence, 100)
	if err != nil || len(next.Events) != 2 || next.Events[0].Sequence != 3 {
		t.Fatal("cannot continue after byte limit", err)
	}
	setText(2, sessions.MaxEventPageBytes)
	if _, err := db.ReadEventPage(context.Background(), "task", 1, 100); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("oversized first event accepted", err)
	}
	prefix, err := db.ReadEventPage(context.Background(), "task", 0, 100)
	if err != nil || len(prefix.Events) != 1 || prefix.NextSequence != 1 || !prefix.HasMore {
		t.Fatal("oversized next event skipped", err)
	}
}

func TestReadEventPageEmptyPageValidatesWholeHead(t *testing.T) {
	for _, query := range []string{
		"UPDATE events SET body=json_set(body,'$.version',2) WHERE sequence=4",
		"UPDATE events SET body=json_set(body,'$.time','0001-01-01T00:00:00Z') WHERE sequence=4",
		"UPDATE events SET body=json_set(body,'$.kind','unknown') WHERE sequence=4",
		"UPDATE events SET body=json_set(body,'$.kind','turn.started','$.turn_id','') WHERE sequence=4",
	} {
		db, _ := eventPageStore(t)
		if _, err := db.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ReadEventPage(context.Background(), "task", 4, 100); !errors.Is(err, sessions.ErrEventPage) {
			t.Fatal("invalid head accepted on empty page", err)
		}
	}
	db, _ := eventPageStore(t)
	if _, err := db.db.Exec("UPDATE events SET body=json_set(body,'$.data.text',?) WHERE sequence=4", strings.Repeat("x", sessions.MaxEventPageBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadEventPage(context.Background(), "task", 4, 100); !errors.Is(err, sessions.ErrEventTooLarge) {
		t.Fatal("oversized head accepted", err)
	}
}
