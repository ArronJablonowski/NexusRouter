package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func appendListedTask(t *testing.T, db *Store, id, session, state string, started time.Time) {
	t.Helper()
	start := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: session, CorrelationID: id, Sequence: 1, Time: started, Kind: runtime.TaskStarted}
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		kind := runtime.TaskCompleted
		if state == "failed" {
			kind = runtime.TaskFailed
		}
		if state == "canceled" {
			kind = runtime.TaskCanceled
		}
		terminal := runtime.Event{Version: 1, ID: id + "-terminal", TaskID: id, SessionID: session, CorrelationID: id, Sequence: 2, Time: started.Add(time.Second), Kind: kind}
		if state == "failed" {
			terminal.Data.Code = "fixture"
		}
		if err := db.Append(context.Background(), 1, terminal); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskListNewestFirstInsertionFence(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	appendListedTask(t, db, "first", "session-1", "completed", time.Unix(100, 0))
	appendListedTask(t, db, "second", "session-2", "running", time.Unix(200, 0))
	page, err := db.ListTasks(ctx, sessions.TaskListOptions{Limit: 1})
	if err != nil || page.Validate() != nil || len(page.Items) != 1 || page.Items[0].TaskID != "second" || !page.HasMore {
		t.Fatal(page, err)
	}
	appendListedTask(t, db, "new", "session-3", "completed", time.Unix(300, 0))
	next, err := db.ListTasks(ctx, sessions.TaskListOptions{After: page.NextCursor, Limit: 1})
	if err != nil || next.Validate() != nil || len(next.Items) != 1 || next.Items[0].TaskID != "first" || next.HasMore {
		t.Fatal(next, err)
	}
	fresh, err := db.ListTasks(ctx, sessions.TaskListOptions{State: "completed", Limit: 10})
	if err != nil || len(fresh.Items) != 2 || fresh.Items[0].TaskID != "new" || fresh.Items[1].TaskID != "first" {
		t.Fatal(fresh, err)
	}
	if _, err = db.ListTasks(ctx, sessions.TaskListOptions{State: "running", After: page.NextCursor, Limit: 1}); !errors.Is(err, sessions.ErrTaskList) {
		t.Fatal("cursor state binding lost", err)
	}
}

func TestTaskListRejectsMalformedStartWithoutPartialPage(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE events SET body=json_set(body,'$.correlation_id','other') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.sequence','1') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.version','1') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE task_heads SET sequence='2' WHERE task_id='corrupt'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "corrupt.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			appendListedTask(t, db, "corrupt", "session-corrupt", "running", time.Unix(100, 0))
			appendListedTask(t, db, "valid", "session-valid", "running", time.Unix(200, 0))
			if _, err = db.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			page, err := db.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
			if err == nil || len(page.Items) != 0 {
				t.Fatal("corrupt task yielded partial page", page, err)
			}
		})
	}
}

func TestTaskListRejectsNonpositiveStorageOrdinal(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "ordinal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	appendListedTask(t, db, "task", "session", "running", time.Unix(100, 0))
	if _, err = db.db.Exec(`UPDATE task_heads SET rowid=0 WHERE task_id='task'`); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListTasks(ctx, sessions.TaskListOptions{Limit: 10})
	if !errors.Is(err, sessions.ErrTaskList) || page.Version != 0 {
		t.Fatal(page, err)
	}
}
