package telemetry

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/diagnostics"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func diagnosticTask(t *testing.T, db *Store, task string) {
	t.Helper()
	for index, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta, runtime.TurnCompleted, runtime.ResponseRevision, runtime.TaskCompleted} {
		event := logEvent(fmt.Sprintf("%s-%d", task, index), task, task+"-session", int64(index+1), kind)
		if index >= 1 && index <= 3 {
			event.TurnID, event.AttemptID = task+"-turn", task+"-attempt"
		}
		if kind == runtime.TaskStarted {
			event.Data = runtime.Data{ModelID: "actual:model", ProviderID: "provider", ContextTokens: 8192, Messages: []providers.Message{{Role: "user", Content: "private request"}}}
		}
		if kind == runtime.TurnStarted {
			event.Data = runtime.Data{ModelID: "actual:model", ProviderID: "provider"}
		}
		if kind == runtime.TurnCompleted {
			event.Data.Text = "private response"
		}
		if kind == runtime.ResponseRevision {
			event.Data.Code, event.Data.Text = "response_contract.v1", "Static correction instructions."
		}
		if err := db.Append(context.Background(), int64(index), event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiagnosticEventsPagesFollowCursorAndTaskFilter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	diagnosticTask(t, db, "a")
	diagnosticTask(t, db, "b")
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	first, err := reader.DiagnosticEvents(ctx, diagnostics.Options{Limit: 2})
	if err != nil || len(first.Records) != 2 || !first.HasMore || first.NextAfter != 2 {
		t.Fatal(first, err)
	}
	second, err := reader.DiagnosticEvents(ctx, diagnostics.Options{After: first.NextAfter, TaskID: "a", Limit: 100, IncludeContent: true})
	if err != nil || len(second.Records) != 3 || second.HasMore || second.NextAfter != 12 || second.Records[0].Kind != runtime.TurnCompleted || second.Records[0].Content.Text != "private response" || second.Records[0].Model != "actual:model" {
		t.Fatal(second, err)
	}
	if second.Records[0].TaskElapsedMS == nil || *second.Records[0].TaskElapsedMS != 3000 || second.Records[0].TurnElapsedMS == nil || *second.Records[0].TurnElapsedMS != 2000 {
		t.Fatal("lost origin metadata", second.Records[0])
	}
	diagnosticTask(t, db, "c")
	third, err := reader.DiagnosticEvents(ctx, diagnostics.Options{After: second.NextAfter, Limit: 100})
	if err != nil || len(third.Records) != 5 || third.Records[0].TaskID != "c" || third.Records[0].Content != nil {
		t.Fatal(third, err)
	}
}

func TestDiagnosticEventsRejectTamperedSelectedLedgerAndInvalidOptions(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	diagnosticTask(t, db, "a")
	for _, options := range []diagnostics.Options{{Limit: 0}, {Limit: 101}, {Limit: 1, After: -1}, {Limit: 1, After: 99}} {
		if _, err := db.DiagnosticEvents(ctx, options); err == nil {
			t.Fatal("invalid options accepted", options)
		}
	}
	if _, err := db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.text','modified') WHERE id='a-3'`); err != nil {
		t.Fatal(err)
	}
	if got, err := db.DiagnosticEvents(ctx, diagnostics.Options{Limit: 100}); err == nil || len(got.Records) != 0 {
		t.Fatal("tampered event released", got, err)
	}
}
