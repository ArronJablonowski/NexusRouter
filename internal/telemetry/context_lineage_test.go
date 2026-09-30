package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func completedCompactedLineageTask(t *testing.T, store *Store, task string) sessions.Snapshot {
	t.Helper()
	ctx := context.Background()
	activation, _, _ := contextCompactionFixture(t, store, task)
	if err := store.Append(ctx, 3, activation); err != nil {
		t.Fatal(err)
	}
	completed := runtime.Event{Version: 1, ID: task + "-complete", TaskID: task, SessionID: activation.SessionID,
		CorrelationID: task, Sequence: 5, Time: activation.Time.Add(time.Second), Kind: runtime.TaskCompleted}
	if err := store.Append(ctx, 4, completed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.TaskSnapshot(ctx, task)
	if err != nil || snapshot.ContextLineage == nil || snapshot.State != "completed" {
		t.Fatal("compacted source did not retain lineage", snapshot, err)
	}
	return snapshot
}

func inheritedLineageStart(source sessions.Snapshot, task string) runtime.Event {
	return runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: source.SessionID,
		CorrelationID: task, Sequence: 1, Time: time.Unix(2000, 0).UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: source.TaskID, ContextLineage: source.ContextLineage,
			Messages: append(append([]providers.Message(nil), source.Messages...), providers.Message{Role: "user", Content: "continue"})}}
}

func TestContextLineageInheritedAdmissionReplayAndConcurrency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lineage.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	source := completedCompactedLineageTask(t, first, "epoch-one")
	start := inheritedLineageStart(source, "epoch-two")
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			results <- store.Append(ctx, 0, start)
		}(store)
	}
	wait.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatal(result)
		}
	}
	var count int
	if err = first.db.QueryRow("SELECT count(*) FROM context_lineage_events WHERE event_id=?", start.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("lineage exact retry duplicated companion", count, err)
	}
	if replayed, replayErr := first.TaskSnapshot(ctx, start.TaskID); replayErr != nil || replayed.ContextLineage == nil || replayed.ContextLineage.Digest != source.ContextLineage.Digest {
		t.Fatal("inherited lineage did not replay", replayed, replayErr)
	}

	foreignSession := inheritedLineageStart(source, "foreign-session")
	foreignSession.SessionID = "other-session"
	if err = first.Append(ctx, 0, foreignSession); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("lineage crossed session boundary", err)
	}
	foreignPrivacy := inheritedLineageStart(source, "foreign-privacy")
	foreignPrivacy.Data.Privacy = "local_only"
	if err = first.Append(ctx, 0, foreignPrivacy); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("lineage crossed privacy boundary", err)
	}
	missing := inheritedLineageStart(source, "missing-lineage")
	missing.Data.ContextLineage = nil
	if err = first.Append(ctx, 0, missing); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("ordinary continuation dropped durable parent lineage", err)
	}
	forged := *source.ContextLineage
	forged.ToolCallIDs = []string{"forged-call"}
	forged.Digest, err = forged.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	fork := inheritedLineageStart(source, "forked-lineage")
	fork.Data.ContextLineage = &forged
	if err = first.Append(ctx, 0, fork); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("forked inherited lineage was admitted", err)
	}
}

func TestContextLineageEligibleRecoveredFailureCanContinue(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "lineage-recovered.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := completedCompactedLineageTask(t, store, "epoch-one")
	history := continuationEvents(true)
	for i := range history {
		history[i].ID = "failed-" + history[i].ID
		history[i].TaskID = "failed-parent"
		history[i].SessionID = source.SessionID
		history[i].CorrelationID = "failed-parent"
		if i == 0 {
			history[i].Data.ParentTaskID = source.TaskID
			history[i].Data.ContextLineage = source.ContextLineage
			history[i].Data.Privacy = source.Privacy
		}
		if i == len(history)-1 {
			history[i].CausationID = history[i-1].ID
		}
		if err = store.Append(ctx, int64(i), history[i]); err != nil {
			t.Fatal(i, err)
		}
	}
	failed, err := store.TaskSnapshot(ctx, "failed-parent")
	if err != nil || failed.State != "failed" || failed.ContextLineage == nil {
		t.Fatal(failed, err)
	}
	child := inheritedLineageStart(failed, "recovered-child")
	if err = store.Append(ctx, 0, child); err != nil {
		t.Fatal("eligible recovered failure could not continue", err)
	}
}

func TestLegacyCompactionCannotDowngradeLineagePredecessor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "lineage-v1-downgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := completedCompactedLineageTask(t, store, "epoch-one")
	legacy := runtime.ContextCompaction{Version: 1, SourceTaskID: source.TaskID, SourceSequence: source.Sequence,
		SourceDigest: source.Compaction.SourceDigest, RemovedMessages: 1, Summary: runtime.ContextSummary{Decisions: []string{"preserve lineage"}}}
	start := runtime.Event{Version: 1, ID: "legacy-start", TaskID: "legacy", SessionID: source.SessionID,
		CorrelationID: "legacy", Sequence: 1, Time: time.Unix(2100, 0).UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: source.TaskID, Compaction: &legacy, Messages: append([]providers.Message(nil), source.Messages...)}}
	if err = start.Validate(); err != nil {
		t.Fatal("invalid downgrade fixture", err)
	}
	if err = store.Append(ctx, 0, start); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("v1 checkpoint erased predecessor lineage", err)
	}
	running := inheritedLineageStart(source, "lineage-current")
	if err = store.Append(ctx, 0, running); err != nil {
		t.Fatal(err)
	}
	legacy.SummaryAttemptID, legacy.SummaryReviewID = "attempt", "review"
	activation := runtime.Event{Version: 1, ID: "legacy-mid-task", TaskID: running.TaskID, SessionID: running.SessionID,
		CorrelationID: running.CorrelationID, Sequence: 2, Time: running.Time.Add(time.Second), Kind: runtime.ContextCompacted,
		Data: runtime.Data{ParentTaskID: source.TaskID, Compaction: &legacy, ReplacedMessages: 1,
			Messages: []providers.Message{{Role: "user", Content: "replacement"}}}}
	if err = activation.Validate(); err != nil {
		t.Fatal("invalid mid-task downgrade fixture", err)
	}
	if err = store.Append(ctx, 1, activation); !errors.Is(err, sessions.ErrHistory) {
		t.Fatal("v1 mid-task checkpoint erased current lineage", err)
	}
}

func TestRunningParentDelegationDoesNotRequireInheritedLineage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "lineage-delegation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent := runtime.Event{Version: 1, ID: "parent-start", TaskID: "parent", SessionID: "session",
		CorrelationID: "parent", Sequence: 1, Time: time.Unix(2200, 0).UTC(), Kind: runtime.TaskStarted}
	if err = store.Append(ctx, 0, parent); err != nil {
		t.Fatal(err)
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	child := runtime.Event{Version: 1, ID: "child-start", TaskID: "child", SessionID: "session",
		CorrelationID: "child", WorkerID: "worker", Sequence: 1, Time: parent.Time.Add(time.Second), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: parent.TaskID, DelegationOrigin: origin}}
	if err = store.Append(ctx, 0, child); err != nil {
		t.Fatal("running-parent delegation was treated as continuation", err)
	}
}

func TestContextLineageMissingCompanionFailsReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lineage-corrupt.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source := completedCompactedLineageTask(t, store, "epoch-one")
	start := inheritedLineageStart(source, "epoch-two")
	if err = store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_lineage_event_immutable_delete;
		DELETE FROM context_lineage_events WHERE event_id=?;
		CREATE TRIGGER context_lineage_event_immutable_delete BEFORE DELETE ON context_lineage_events
		BEGIN SELECT RAISE(ABORT,'context lineage event immutable'); END;`, start.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("event without lineage companion reopened")
	}
}

func TestContextLineageForkedPredecessorFailsReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lineage-fork.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source := completedCompactedLineageTask(t, store, "epoch-one")
	start := inheritedLineageStart(source, "epoch-two")
	if err = store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	forged := *source.ContextLineage
	forged.ToolCallIDs = append(append([]string(nil), forged.ToolCallIDs...), "forked-call")
	forged.Digest, err = forged.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	start.Data.ContextLineage = &forged
	eventBody, err := start.Encode()
	if err != nil {
		t.Fatal(err)
	}
	lineageBody, err := json.Marshal(&forged)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the event and every normalized column mutually consistent. Reopen
	// must still derive the durable parent snapshot and reject the fork.
	if _, err = store.db.Exec(`DROP TRIGGER context_lineage_event_immutable_update;
		UPDATE events SET body=? WHERE id=?;
		UPDATE context_lineage_events SET lineage_digest=?,previous_digest=?,body=? WHERE event_id=?;
		CREATE TRIGGER context_lineage_event_immutable_update BEFORE UPDATE ON context_lineage_events
		BEGIN SELECT RAISE(ABORT,'context lineage event immutable'); END;`, eventBody, start.ID,
		forged.Digest, forged.Digest, lineageBody, start.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("self-consistent forked lineage reopened")
	}
}

func TestContextLineageMigrationFromSchema50(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lineage-migration.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_lineage_event_binding;
		DROP TRIGGER context_lineage_event_immutable_delete;
		DROP TRIGGER context_lineage_event_immutable_update;
		DROP INDEX context_lineage_events_digest;
		DROP TABLE context_lineage_events;
		PRAGMA user_version=50;`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, tables int
	if err = store.db.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM sqlite_master WHERE type='table' AND name='context_lineage_events')`).Scan(&version, &tables); err != nil || version != stateschema.Current || tables != 1 {
		t.Fatal("schema-50 migration did not reach lineage schema", version, tables, err)
	}
}

func TestContextLineagePartialFutureSchemaRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lineage-partial.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_lineage_event_binding;
		DROP TRIGGER context_lineage_event_immutable_delete;
		DROP TRIGGER context_lineage_event_immutable_update;
		DROP INDEX context_lineage_events_digest;
		DROP TABLE context_lineage_events;
		CREATE TABLE context_lineage_events(sentinel TEXT);
		PRAGMA user_version=50;`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("partial future lineage declaration reopened")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, columns int
	if err = raw.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT count(*) FROM pragma_table_info('context_lineage_events'))`).Scan(&version, &columns); err != nil || version != 50 || columns != 1 {
		t.Fatal("failed migration did not roll back", version, columns, err)
	}
}
