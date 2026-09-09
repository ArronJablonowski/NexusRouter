package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func appendSessionTask(t *testing.T, db *Store, id, session, parent, retry, state string, started time.Time) {
	t.Helper()
	start := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: session, CorrelationID: id, Sequence: 1, Time: started, Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: parent, RetryOfTaskID: retry, Messages: []providers.Message{{Role: "user", Content: "private prompt " + id}}}}
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	if state == "running" {
		return
	}
	kind := runtime.TaskCompleted
	if state == "failed" {
		kind = runtime.TaskFailed
	}
	if state == "canceled" {
		kind = runtime.TaskCanceled
	}
	terminal := runtime.Event{Version: 1, ID: id + "-terminal", TaskID: id, SessionID: session, CorrelationID: id, Sequence: 2, Time: started.Add(time.Second), Kind: kind, Data: runtime.Data{Text: "private output " + id}}
	if state == "failed" {
		terminal.Data.Code = "fixture_failure"
	}
	if err := db.Append(context.Background(), 1, terminal); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTasksExactTopologyContentFreeAndInsertionFenced(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := time.Unix(100, 0).UTC()
	appendSessionTask(t, db, "root", "session", "", "", "completed", base)
	appendSessionTask(t, db, "branch-a", "session", "root", "", "failed", base.Add(time.Second))
	appendSessionTask(t, db, "unrelated", "other-session", "", "", "completed", base.Add(2*time.Second))
	safeRetryFailure(t, db, "prior", "")
	appendSessionTask(t, db, "fallback", "session", "root", "prior", "completed", base.Add(3*time.Second))

	before, err := db.Read(ctx, "root", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	page, err := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 2})
	if err != nil || page.Validate() != nil || !page.HasMore || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	if page.Items[0].TaskID != "fallback" || page.Items[0].ParentTaskID != "root" || page.Items[0].RetryOfTaskID != "prior" || page.Items[1].TaskID != "branch-a" || page.Items[1].ParentTaskID != "root" {
		t.Fatal("lineage projection mismatch", page.Items)
	}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private prompt", "private output", "messages", "tool_calls"} {
		if strings.Contains(string(body), private) {
			t.Fatal("session metadata leaked content", string(body))
		}
	}

	appendSessionTask(t, db, "new-branch", "session", "root", "", "completed", base.Add(4*time.Second))
	next, err := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{After: page.NextCursor, Limit: 2})
	if err != nil || next.Validate() != nil || next.HasMore || len(next.Items) != 1 || next.Items[0].TaskID != "root" || next.Items[0].ParentTaskID != "" || next.Items[0].RetryOfTaskID != "" {
		t.Fatal("session insertion fence or root topology failed", next, err)
	}
	fresh, err := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 10})
	if err != nil || len(fresh.Items) != 4 || fresh.Items[0].TaskID != "new-branch" {
		t.Fatal("fresh session page missed new branch", fresh, err)
	}
	after, err := db.Read(ctx, "root", 0, 10)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("session inspection mutated source", err)
	}
	other, err := db.ListSessionTasks(ctx, "other-session", sessions.SessionTaskListOptions{Limit: 10})
	if err != nil || len(other.Items) != 1 || other.Items[0].TaskID != "unrelated" {
		t.Fatal("session membership leaked", other, err)
	}
}

func TestSessionTasksRejectsUnsafeRetryAndNoncanonicalStart(t *testing.T) {
	for _, mutation := range []string{"unsafe-retry", "duplicate-parent"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "lineage.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			appendSessionTask(t, db, "root", "session", "", "", "completed", time.Unix(90, 0).UTC())
			appendSessionTask(t, db, "unsafe", "other", "", "", "completed", time.Unix(95, 0).UTC())
			appendSessionTask(t, db, "child", "session", "root", "unsafe", "completed", time.Unix(100, 0).UTC())
			if mutation == "duplicate-parent" {
				var raw []byte
				if err = db.db.QueryRow(`SELECT body FROM events WHERE task_id='child' AND sequence=1`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				changed := strings.Replace(string(raw), `"parent_task_id":"root"`, `"parent_task_id":"other","parent_task_id":"root"`, 1)
				if changed == string(raw) {
					t.Fatal("fixture did not inject duplicate key")
				}
				if _, err = db.db.Exec(`UPDATE events SET body=? WHERE task_id='child' AND sequence=1`, changed); err != nil {
					t.Fatal(err)
				}
			}
			page, listErr := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 1})
			if !errors.Is(listErr, sessions.ErrSessionTasks) || len(page.Items) != 0 {
				t.Fatal("unsafe or noncanonical lineage escaped", page, listErr)
			}
		})
	}
}

func TestSessionTasksRejectCorruptLineageAndEnvelopeWithoutPartialPage(t *testing.T) {
	mutations := []string{
		`UPDATE events SET body=json_set(body,'$.session_id','other') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.data.parent_task_id',7) WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.data.parent_task_id','corrupt') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.data.retry_of_task_id','corrupt') WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE events SET body=json_set(body,'$.data.parent_task_id',?) WHERE task_id='corrupt' AND sequence=1`,
		`UPDATE task_heads SET sequence=3 WHERE task_id='corrupt'`,
		`UPDATE events SET body=json_set(body,'$.data.parent_task_id','foreign') WHERE task_id='corrupt' AND sequence=1`,
	}
	for i, mutation := range mutations {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "corrupt.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			appendSessionTask(t, db, "root", "session", "", "", "completed", time.Unix(90, 0).UTC())
			appendSessionTask(t, db, "foreign", "other", "", "", "completed", time.Unix(95, 0).UTC())
			appendSessionTask(t, db, "corrupt", "session", "root", "", "completed", time.Unix(100, 0).UTC())
			appendSessionTask(t, db, "valid", "session", "", "", "completed", time.Unix(200, 0).UTC())
			var execErr error
			if strings.Contains(mutation, "?") {
				_, execErr = db.db.Exec(mutation, strings.Repeat("x", 129))
			} else {
				_, execErr = db.db.Exec(mutation)
			}
			if execErr != nil {
				t.Fatal(execErr)
			}
			page, listErr := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 10})
			if !errors.Is(listErr, sessions.ErrSessionTasks) || len(page.Items) != 0 {
				t.Fatal("corruption yielded a partial session page", page, listErr)
			}
		})
	}
}

func TestSessionTasksRejectsCorruptRelatedTaskOutsidePage(t *testing.T) {
	for _, target := range []string{"start", "head"} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "related.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			appendSessionTask(t, db, "root", "session", "", "", "completed", time.Unix(90, 0).UTC())
			appendSessionTask(t, db, "child", "session", "root", "", "completed", time.Unix(100, 0).UTC())
			sequence := 1
			if target == "head" {
				sequence = 2
			}
			if _, err = db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='root' AND sequence=?`, sequence); err != nil {
				t.Fatal(err)
			}
			page, err := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 1})
			if !errors.Is(err, sessions.ErrSessionTasks) || len(page.Items) != 0 {
				t.Fatal("noncanonical related task escaped through a smaller page", page, err)
			}
		})
	}
}

func TestSessionTasksRejectsNoncanonicalHeadAndNonchronologicalRetryChain(t *testing.T) {
	for _, mutation := range []string{"head", "retry-order"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "chronology.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if mutation == "retry-order" {
				safeRetryFailure(t, db, "old", "later")
				safeRetryFailure(t, db, "later", "")
				appendSessionTask(t, db, "child", "session", "", "old", "completed", time.Unix(100, 0).UTC())
			} else {
				appendSessionTask(t, db, "child", "session", "", "", "completed", time.Unix(100, 0).UTC())
				if _, err = db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='child' AND sequence=2`); err != nil {
					t.Fatal(err)
				}
			}
			page, listErr := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 1})
			if !errors.Is(listErr, sessions.ErrSessionTasks) || len(page.Items) != 0 {
				t.Fatal("noncanonical head or nonchronological retry escaped", page, listErr)
			}
		})
	}
}

func TestSessionTasksRejectsMisboundRetryLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "misbound.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	safeRetryFailure(t, db, "prior", "")
	var raw []byte
	if err = db.db.QueryRow(`SELECT body FROM events WHERE task_id='prior' AND sequence=3`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var terminal runtime.Event
	if json.Unmarshal(raw, &terminal) != nil {
		t.Fatal("invalid fixture event")
	}
	terminal.TurnID = "other-turn"
	changed, err := terminal.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE events SET body=? WHERE task_id='prior' AND sequence=3`, changed); err != nil {
		t.Fatal(err)
	}
	appendSessionTask(t, db, "child", "session", "", "prior", "completed", time.Unix(100, 0).UTC())
	page, listErr := db.ListSessionTasks(ctx, "session", sessions.SessionTaskListOptions{Limit: 1})
	if !errors.Is(listErr, sessions.ErrSessionTasks) || len(page.Items) != 0 {
		t.Fatal("misbound retry lifecycle escaped", page, listErr)
	}
}

func TestSessionTasksAdmissionEmptyAndCancellation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.ListSessionTasks(ctx, "missing-session", sessions.SessionTaskListOptions{Limit: 25})
	if err != nil || page.Validate() != nil || len(page.Items) != 0 || page.SessionID != "missing-session" {
		t.Fatal(page, err)
	}
	for _, session := range []string{"", "bad:session", strings.Repeat("x", 129)} {
		if _, err := db.ListSessionTasks(ctx, session, sessions.SessionTaskListOptions{Limit: 25}); !errors.Is(err, sessions.ErrSessionTasks) {
			t.Fatal("invalid session admitted", session, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.ListSessionTasks(canceled, "session", sessions.SessionTaskListOptions{Limit: 25}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity lost", err)
	}
}
