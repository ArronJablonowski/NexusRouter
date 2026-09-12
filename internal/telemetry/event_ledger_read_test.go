package telemetry

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestGenericReadAndTaskSnapshotRejectNoncanonicalLedgerWithoutPartialResults(t *testing.T) {
	readers := []struct {
		name string
		read func(context.Context, *Store) (bool, error)
	}{
		{
			name: "generic read",
			read: func(ctx context.Context, store *Store) (bool, error) {
				events, err := store.Read(ctx, "task", 0, 100)
				return len(events) == 0, err
			},
		},
		{
			name: "task snapshot",
			read: func(ctx context.Context, store *Store) (bool, error) {
				snapshot, err := store.TaskSnapshot(ctx, "task")
				return reflect.DeepEqual(snapshot, sessions.Snapshot{}), err
			},
		},
	}
	mutations := []struct {
		name   string
		mutate func(*testing.T, *Store)
	}{
		{
			name: "missing ledger row",
			mutate: func(t *testing.T, store *Store) {
				if _, err := store.db.Exec(`DELETE FROM event_log WHERE task_id='task' AND task_sequence=2`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "noncontiguous ledger position",
			mutate: func(t *testing.T, store *Store) {
				if _, err := store.db.Exec(`UPDATE event_log SET position=position+10 WHERE task_id='task' AND task_sequence=2`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong ledger digest",
			mutate: func(t *testing.T, store *Store) {
				if _, err := store.db.Exec(`UPDATE event_log SET body_digest=lower(hex(randomblob(32))) WHERE task_id='task' AND task_sequence=2`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "noncanonical body with matching digest",
			mutate: func(t *testing.T, store *Store) {
				var body []byte
				if err := store.db.QueryRow(`SELECT body FROM events WHERE task_id='task' AND sequence=2`).Scan(&body); err != nil {
					t.Fatal(err)
				}
				body = append(body, ' ')
				if _, err := store.db.Exec(`UPDATE events SET body=? WHERE task_id='task' AND sequence=2; UPDATE event_log SET body_digest=? WHERE task_id='task' AND task_sequence=2`, body, streamBodyDigest(body)); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, reader := range readers {
		for _, mutation := range mutations {
			t.Run(reader.name+"/"+mutation.name, func(t *testing.T) {
				store, _ := eventPageStore(t)
				mutation.mutate(t, store)
				empty, err := reader.read(context.Background(), store)
				if !empty || err == nil || !errors.Is(err, sessions.ErrEventLog) && !errors.Is(err, sessions.ErrHistory) {
					t.Fatalf("noncanonical ledger accepted: %v", err)
				}
			})
		}
	}
}

func TestGenericReadReturnsCanonicalLedgerPage(t *testing.T) {
	store, _ := eventPageStore(t)
	events, err := store.Read(context.Background(), "task", 1, 2)
	if err != nil || len(events) != 2 || events[0].Kind != runtime.TurnStarted || events[1].Kind != runtime.TurnCompleted {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
