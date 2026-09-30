package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func cliSummaryRecoveryFixture(t *testing.T) (string, sessions.SummaryAttempt, sessions.SummaryRecovery) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "summary-recoveries.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		event := runtime.Event{Version: 1, ID: string(kind), TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: int64(i + 1), Time: now, Kind: kind}
		if kind == runtime.TaskStarted {
			event.Data.Messages = []providers.Message{{Role: "user", Content: "source requirement"}}
		}
		if kind == runtime.TurnStarted || kind == runtime.TurnCompleted {
			event.TurnID, event.AttemptID = "turn", "attempt"
		}
		if kind == runtime.TurnCompleted {
			event.Data.Text = "source result"
		}
		if err = db.Append(ctx, int64(i), event); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	source, err := sessions.Replay(ctx, db, "source")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	request := sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"preserve requirement"}}}
	_, checkpoint, err := sessions.PrepareContinuation(source, request)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	attempt := sessions.SummaryAttempt{Version: 1, ID: "summary-attempt", TaskID: "source", SourceDigest: checkpoint.SourceDigest, Model: "model", Provider: "provider", Status: "started", SourceSequence: source.Sequence, Keep: 1, StartedAt: now}
	if err = db.BeginSummary(ctx, attempt); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.Code, attempt.FinishedAt = "interrupted", "owner_interrupted", now.Add(time.Second)
	recovery := sessions.SummaryRecovery{Version: 1, ID: strings.Repeat("a", 64), AttemptID: attempt.ID, TaskID: attempt.TaskID, SourceDigest: attempt.SourceDigest, SourceSequence: attempt.SourceSequence, State: "interrupted", Code: "owner_interrupted", RecoveredAt: now.Add(time.Second)}
	attemptBody, _ := json.Marshal(attempt)
	recoveryBody, _ := json.Marshal(recovery)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE summary_attempts SET body=? WHERE id=?`, attemptBody, attempt.ID); err == nil {
		_, err = raw.ExecContext(ctx, `INSERT INTO summary_attempt_recoveries(id,attempt_id,task_id,recovered_at,body) VALUES(?,?,?,?,?)`, recovery.ID, recovery.AttemptID, recovery.TaskID, recovery.RecoveredAt.UnixNano(), recoveryBody)
	}
	closeErr := raw.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return path, attempt, recovery
}

func TestSummaryRecoveriesCLIExactAndListAreReadOnly(t *testing.T) {
	path, _, want := cliSummaryRecoveryFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		list bool
	}{
		{args: []string{"summaries", "recoveries", "--db", path, "--id", want.AttemptID}},
		{args: []string{"summaries", "recoveries", "--db", path, "--task", want.TaskID, "--limit", "1"}, list: true},
	} {
		var out, diagnostic bytes.Buffer
		if code := Run(test.args, &out, &diagnostic, "test"); code != 0 || diagnostic.Len() != 0 {
			t.Fatal(code, diagnostic.String())
		}
		if test.list {
			var got []sessions.SummaryRecovery
			if json.Unmarshal(out.Bytes(), &got) != nil || !reflect.DeepEqual(got, []sessions.SummaryRecovery{want}) {
				t.Fatal(out.String())
			}
		} else {
			var got sessions.SummaryRecovery
			if json.Unmarshal(out.Bytes(), &got) != nil || got != want {
				t.Fatal(out.String())
			}
		}
	}
	var diagnostic bytes.Buffer
	if code := Run([]string{"summaries", "recoveries", "--db", path, "--id", want.AttemptID}, brokenWriter{}, &diagnostic, "test"); code != 1 {
		t.Fatal("recovery output failure ignored", code)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recovery inspection changed storage", err)
	}
}

func TestSummaryRecoveriesCLIArgumentsAndFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, args := range [][]string{
		{"recoveries"},
		{"recoveries", "--db", path, "--id", ""},
		{"recoveries", "--db", path, "--id", "attempt", "--task", "source"},
		{"recoveries", "--db", path, "--id", "attempt", "--after", "receipt"},
		{"recoveries", "--db", path, "--id", "attempt", "--limit", "1"},
		{"recoveries", "--db", path, "--limit", "0"},
		{"recoveries", "--db", path, "--limit", "101"},
		{"recover", "--db", path, "--id", "attempt"},
		{"reconcile", "--db", path},
	} {
		var out, diagnostic bytes.Buffer
		if code := runSummaries(args, &out, &diagnostic); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), diagnostic.String())
		}
	}
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"summaries", "recoveries", "--db", path, "--id", "missing"}, &out, &diagnostic, "test"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), path) {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only recovery inspection created storage", err)
	}
	if code := Run([]string{"summaries", "recoveries", "--db", path}, brokenWriter{}, &diagnostic, "test"); code != 1 {
		t.Fatal("missing database should fail before output", code)
	}
}
