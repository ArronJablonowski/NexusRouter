package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestTaskInspection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now(), Kind: kind, TurnID: "turn", AttemptID: "attempt"}
		if i == 0 {
			e.Data.Messages = []providers.Message{{Role: "user", Content: "question"}}
		}
		if i == 2 {
			e.Data.Text = "answer"
		}
		if err := db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := Run([]string{"task", "show", "--db", path, "--task", "task"}, &out, &errout, "dev")
	if code != 0 {
		t.Fatal(code, errout.String())
	}
	var s sessions.Snapshot
	if json.Unmarshal(out.Bytes(), &s) != nil || s.State != "completed" || len(s.Messages) != 2 || s.Messages[1].Content != "answer" {
		t.Fatal(out.String())
	}
	if Run([]string{"task", "show", "--db", path, "--task", "task"}, brokenWriter{}, &errout, "dev") != 1 {
		t.Fatal("output failure ignored")
	}
	missing := filepath.Join(t.TempDir(), "nonexistent.db")
	if Run([]string{"task", "show", "--db", missing, "--task", "task"}, &out, &errout, "dev") != 1 {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created database")
	}
	ro, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	e := runtime.Event{Version: 1, ID: "other", TaskID: "other", SessionID: "session", CorrelationID: "other", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err := ro.Append(ctx, 0, e); err == nil {
		t.Fatal("read-only store allowed mutation")
	}
}
