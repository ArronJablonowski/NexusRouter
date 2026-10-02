package usagestats_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteProjectionOwnTasksBothLocalitiesAndPartialFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, e := telemetry.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	add := func(task, provider string, counts []*providers.Usage, terminal runtime.Kind) {
		t.Helper()
		seq := int64(0)
		now := time.Now().UTC()
		emit := func(kind runtime.Kind, turn string, data runtime.Data) {
			seq++
			event := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-%d", task, seq), TaskID: task, SessionID: task, CorrelationID: task, Sequence: seq, Time: now.Add(time.Duration(seq) * time.Millisecond), Kind: kind, Data: data}
			if turn != "" {
				event.TurnID = turn
				event.AttemptID = turn + "-attempt"
			}
			if e := store.Append(ctx, seq-1, event); e != nil {
				t.Fatal(e)
			}
		}
		emit(runtime.TaskStarted, "", runtime.Data{ProviderID: provider, ModelID: "model"})
		for i, u := range counts {
			turn := fmt.Sprintf("turn-%d", i)
			emit(runtime.TurnStarted, turn, runtime.Data{ProviderID: provider, ModelID: "model"})
			emit(runtime.TurnCompleted, turn, runtime.Data{Usage: u})
		}
		emit(terminal, "", runtime.Data{})
	}
	add("local", "local", []*providers.Usage{{InputTokens: 10, OutputTokens: 2}}, runtime.TaskCompleted)
	add("cloud", "cloud", []*providers.Usage{{InputTokens: 20, OutputTokens: 3}, nil}, runtime.TaskFailed)
	add("unrelated", "local", []*providers.Usage{{InputTokens: 900, OutputTokens: 900}}, runtime.TaskCompleted)
	kinds := map[[2]string]string{{"local", "model"}: "local", {"cloud", "model"}: "cloud"}
	got, e := usagestats.TaskRemoteUsage(ctx, path, kinds, []string{"local", "cloud", "local"})
	if e != nil || got.Local.Input != "10" || got.Cloud.Input != "20" || got.Cloud.Unknown != 1 || got.Cloud.Partial != 1 {
		t.Fatal(got, e)
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var raw []byte
	if e = db.QueryRow("SELECT body FROM usage_records WHERE task_id='local'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var original accounting.Record
	if e = json.Unmarshal(raw, &original); e != nil {
		t.Fatal(e)
	}
	corrected := accounting.CloneRecord(original)
	corrected.ID = "local-correction"
	corrected.Usage = &providers.Usage{InputTokens: 8, OutputTokens: 1}
	correction := accounting.Correction{Version: 1, ID: corrected.ID, BaseID: original.ID, Supersedes: original.ID, Evidence: "provider-receipt", Reason: accounting.ProviderReconciliation, Record: corrected, RecordedAt: original.OccurredAt.Add(time.Second)}
	if e = store.CorrectUsage(ctx, correction); e != nil {
		t.Fatal(e)
	}
	got, e = usagestats.TaskRemoteUsage(ctx, path, kinds, []string{"local", "cloud"})
	if e != nil || got.Local.Input != "8" || got.Local.Output != "1" {
		t.Fatal(got, e)
	}
	got, e = usagestats.TaskRemoteUsage(ctx, path, kinds, []string{"missing"})
	if e != nil || got.Unavailable != 1 {
		t.Fatal(got, e)
	}
}
