package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const browserTestSubject = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBrowserMigrationOperationCapacityPreservesPending(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := time.Now().UTC().Add(-BrowserOperationRetention - time.Hour).UnixNano()
	for i := 0; i < MaxBrowserOperations; i++ {
		id := fmt.Sprintf("op_%064x", i+1)
		_, err = store.db.Exec(`INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?)`, id, browserTestSubject, fmt.Sprintf("%064x", i+1), "submit", fmt.Sprintf("%064x", i+2), old, old)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.BeginBrowserOperation(ctx, browserTestSubject, "browser-operation-capacity", "submit", []byte(`{"text":"new"}`))
	if !errors.Is(err, ErrBrowserOperationCapacity) {
		t.Fatal("hard cap not enforced", err)
	}
	var pending int
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations WHERE state='pending'`).Scan(&pending); err != nil || pending != MaxBrowserOperations {
		t.Fatal("pending rows pruned", pending, err)
	}
	if _, err = store.db.Exec(`DELETE FROM browser_operations`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxBrowserOperations; i++ {
		id := fmt.Sprintf("op_%064x", i+1)
		_, err = store.db.Exec(`INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,response,created_at,updated_at) VALUES(?,?,?,?,?,'rejected','{}',?,?)`, id, browserTestSubject, fmt.Sprintf("%064x", i+1), "submit", fmt.Sprintf("%064x", i+2), old, old)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.BeginBrowserOperation(ctx, browserTestSubject, "browser-operation-after-prune", "submit", []byte(`{"text":"new"}`)); err != nil {
		t.Fatal("rejected retention did not free capacity", err)
	}
	var rejected int
	if err = store.db.QueryRow(`SELECT count(*) FROM browser_operations WHERE state='rejected'`).Scan(&rejected); err != nil || rejected != 0 {
		t.Fatal("expired rejected rows retained", rejected, err)
	}
}

func TestBrowserMigrationRejectsPartialShapeAndRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TABLE browser_feedback; DROP TABLE browser_operations; CREATE TABLE browser_operations(sentinel TEXT); PRAGMA user_version=33`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("partial v34 schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err = raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 33 {
		t.Fatal("failed migration advanced", version, err)
	}
	var sentinel int
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_table_info('browser_operations') WHERE name='sentinel'`).Scan(&sentinel); err != nil || sentinel != 1 {
		t.Fatal("partial table was replaced", sentinel, err)
	}
}

func TestBrowserFeedbackCoexistsAndCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Append(ctx, 0, event("start", 1, runtime.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	turn := event("turn", 2, runtime.TurnStarted)
	turn.TurnID, turn.AttemptID, turn.Data.ModelID, turn.Data.ProviderID = "turn", "attempt", "model", "provider"
	if err = store.Append(ctx, 1, turn); err != nil {
		t.Fatal(err)
	}
	turnDone := event("turn-done", 3, runtime.TurnCompleted)
	turnDone.TurnID, turnDone.AttemptID = "turn", "attempt"
	if err = store.Append(ctx, 2, turnDone); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 3, event("done", 4, runtime.TaskCompleted)); err != nil {
		t.Fatal(err)
	}
	evidence := evaluation.Record{Version: 1, ID: "objective", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "code", Profile: "default"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "tests", Passed: true}, {Source: evaluation.LLMJudge, Reference: "audit", Passed: true}}, ExecutionSucceeded: true, Time: time.Now().UTC()}
	if err = store.RecordEvaluation(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	first := BrowserFeedback{Version: 1, ID: "feedback_one", TaskID: "task", Accepted: true, AttemptCost: .2, CreatedAt: time.Now().UTC()}
	if err = store.AppendBrowserFeedback(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	second := BrowserFeedback{Version: 1, ID: "feedback_two", TaskID: "task", Supersedes: first.ID, Accepted: false, AttemptCost: .2, CreatedAt: first.CreatedAt.Add(time.Nanosecond)}
	if err = store.AppendBrowserFeedback(ctx, second, 1); err != nil {
		t.Fatal(err)
	}
	history, err := store.BrowserFeedbackHistory(ctx, "task")
	if err != nil || len(history) != 2 || history[1].Accepted {
		t.Fatal(history, err)
	}
	evaluationHistory, err := store.EvaluationHistory(ctx, "task", "attempt")
	if err != nil || len(evaluationHistory) != 1 || len(evaluationHistory[0].Checks) != 2 || evaluationHistory[0].Checks[0].Source != evaluation.Deterministic || evaluationHistory[0].Checks[1].Source != evaluation.LLMJudge {
		t.Fatal("evaluation evidence changed", evaluationHistory, err)
	}
	if _, err = store.db.Exec(`UPDATE browser_feedback SET body='{}' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if history, err = store.BrowserFeedbackHistory(ctx, "task"); !errors.Is(err, ErrBrowserFeedback) || history != nil {
		t.Fatal("corruption accepted", history, err)
	}
}
