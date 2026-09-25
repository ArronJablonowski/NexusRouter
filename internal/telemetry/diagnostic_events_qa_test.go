package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestDiagnosticsResumeAfterBoundedSuppressedOnlyPage(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "diagnostics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	appendEvent := func(sequence int64, kind runtime.Kind) {
		t.Helper()
		event := logEvent(fmt.Sprintf("event-%d", sequence), "task", "session", sequence, kind)
		if kind == runtime.TurnStarted || kind == runtime.TurnCompleted || kind == runtime.ModelDelta {
			event.TurnID, event.AttemptID = "turn", "attempt"
		}
		if err := db.Append(ctx, sequence-1, event); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(1, runtime.TaskStarted)
	appendEvent(2, runtime.TurnStarted)
	for sequence := int64(3); sequence <= 1003; sequence++ {
		appendEvent(sequence, runtime.ModelDelta)
	}
	appendEvent(1004, runtime.TurnCompleted)
	appendEvent(1005, runtime.TaskCompleted)
	first, err := db.DiagnosticEvents(ctx, diagnostics.Options{After: 2, Limit: 100})
	if err != nil || len(first.Records) != 0 || first.NextAfter != 1002 || !first.HasMore {
		t.Fatalf("suppressed page was unbounded or lost its resume position: %+v, %v", first, err)
	}
	second, err := db.DiagnosticEvents(ctx, diagnostics.Options{After: first.NextAfter, Limit: 100})
	if err != nil || len(second.Records) != 2 || second.Records[0].Position != 1004 || second.Records[1].Position != 1005 || second.NextAfter != 1005 || second.HasMore {
		t.Fatalf("completion after suppressed page was lost or duplicated: %+v, %v", second, err)
	}
	empty, err := db.DiagnosticEvents(ctx, diagnostics.Options{After: second.NextAfter, Limit: 100})
	if err != nil || len(empty.Records) != 0 || empty.NextAfter != second.NextAfter || empty.HasMore {
		t.Fatalf("empty follow poll replayed prior records: %+v, %v", empty, err)
	}
}

func TestDiagnosticsDoNotCheckpointPastDisappearedSourceEvent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "diagnostics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := logEvent("start", "task", "session", 1, runtime.TaskStarted)
	done := logEvent("done", "task", "session", 2, runtime.TaskCompleted)
	for i, event := range []runtime.Event{start, done} {
		if err := db.Append(ctx, int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM events WHERE id='done'"); err != nil {
		t.Fatal(err)
	}
	page, err := db.DiagnosticEvents(ctx, diagnostics.Options{Limit: 100})
	if err == nil {
		t.Fatalf("silently checkpointed beyond missing durable event: %+v", page)
	}
}

func TestDiagnosticsAuthenticateFilteredEventKindsBeforeSkipping(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "diagnostics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := logEvent("start", "task", "session", 1, runtime.TaskStarted)
	done := logEvent("done", "task", "session", 2, runtime.TaskCompleted)
	for i, event := range []runtime.Event{start, done} {
		if err := db.Append(ctx, int64(i), event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE events SET body=json_set(body,'$.kind','model.delta') WHERE id='done'"); err != nil {
		t.Fatal(err)
	}
	page, err := db.DiagnosticEvents(ctx, diagnostics.Options{After: 1, Limit: 100})
	if err == nil {
		t.Fatalf("unauthenticated kind hid corrupt event from diagnostics: %+v", page)
	}
}

func TestDiagnosticsRejectDeletedLedgerEntryInsteadOfSkippingIt(t *testing.T) {
	for _, options := range []diagnostics.Options{{Limit: 100}, {After: 2, Limit: 100}, {TaskID: "task", Limit: 100}} {
		t.Run(fmt.Sprintf("after-%d-task-%s", options.After, options.TaskID), func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "diagnostics.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.ErrorRecorded, runtime.TaskCompleted} {
				event := logEvent(fmt.Sprintf("event-%d", i), "task", "session", int64(i+1), kind)
				if kind == runtime.ErrorRecorded {
					event.Data.Code = "temporary_error"
				}
				if err := db.Append(ctx, int64(i), event); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.db.ExecContext(ctx, "DELETE FROM event_log WHERE position=2"); err != nil {
				t.Fatal(err)
			}
			page, err := db.DiagnosticEvents(ctx, options)
			if err == nil || len(page.Records) != 0 {
				t.Fatalf("checkpoint skipped a missing ledger record: %+v, %v", page, err)
			}
		})
	}
}
