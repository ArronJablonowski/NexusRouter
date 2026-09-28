package usagestats

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestPartialUsageLongStreamUsesSelectedRowBound(t *testing.T) {
	for _, excessive := range []bool{false, true} {
		t.Run(fmt.Sprintf("excessive_usage_rows_%v", excessive), func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TABLE events(id TEXT,task_id TEXT,sequence INTEGER,body BLOB); CREATE INDEX event_task_sequence ON events(task_id,sequence)"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			now := time.Now().UTC()
			seq := int64(0)
			appendEvent := func(kind runtime.Kind, turn string, data runtime.Data) runtime.Event {
				t.Helper()
				seq++
				e := runtime.Event{Version: 1, ID: fmt.Sprintf("event-%d", seq), TaskID: "task", SessionID: "task", CorrelationID: "task", Sequence: seq, Time: now.Add(time.Duration(seq) * time.Nanosecond), Kind: kind, Data: data}
				if turn != "" {
					e.TurnID = turn
					e.AttemptID = turn + "-attempt"
				}
				if err := e.Validate(); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(e)
				if _, err := tx.Exec("INSERT INTO events VALUES(?,?,?,?)", e.ID, e.TaskID, e.Sequence, body); err != nil {
					t.Fatal(err)
				}
				return e
			}
			start := runtime.Data{ProviderID: "provider", ModelID: "model"}
			appendEvent(runtime.TaskStarted, "", start)
			appendEvent(runtime.TurnStarted, "first", start)
			appendEvent(runtime.TurnCompleted, "first", runtime.Data{Usage: &providers.Usage{InputTokens: 10, OutputTokens: 2}, FinishReason: "stop"})
			appendEvent(runtime.TurnStarted, "stream", start)
			for i := 0; i < sessions.MaxTaskEvents; i++ {
				if excessive {
					appendEvent(runtime.TurnStarted, fmt.Sprintf("extra-%d", i), start)
				} else {
					appendEvent(runtime.ModelDelta, "stream", runtime.Data{Text: "x"})
				}
			}
			terminal := appendEvent(runtime.TaskFailed, "stream", runtime.Data{Code: "execution_failed"})
			r := accounting.Record{Version: 1, ID: "usage", TaskID: "task", SessionID: "task", OperationID: "task", RouteID: "route", EvidenceID: terminal.ID, Provider: "provider", Model: "model", Role: accounting.PrimaryExecution, EvidenceKind: accounting.EventEvidence, Disposition: accounting.Failed, RetryClass: accounting.Retryable, OccurredAt: terminal.Time}
			usage, err := measuredTurns(context.Background(), tx, r)
			if excessive {
				if err == nil {
					t.Fatal("unbounded usage event selection accepted")
				}
				return
			}
			if err != nil || usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 2 {
				t.Fatalf("long stream lost measured usage: %+v, %v", usage, err)
			}
		})
	}
}
