package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func atomicStart(worker, task, session string, at time.Time) runtime.Event {
	return runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: session, CorrelationID: task,
		WorkerID: worker, Sequence: 1, Time: at.UTC(), Kind: runtime.TaskStarted}
}

func atomicClaimService(t *testing.T, store *Store, worker string, clock *time.Time) *workboard.LifecycleService {
	t.Helper()
	return newTestLifecycleService(t, store, workboard.Actor{ID: worker, Type: "worker"}, verifiedLifecycleRecovery("unused-atomic-proof", workboard.EffectFree), clock)
}

func atomicClaimServiceWithTTL(t *testing.T, store *Store, worker string, clock *time.Time, ttl time.Duration) *workboard.LifecycleService {
	t.Helper()
	service, err := workboard.NewLifecycleService(store,
		telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: worker, Type: "worker"}}},
		verifiedLifecycleRecovery("unused-atomic-proof", workboard.EffectFree), func() time.Time { return *clock }, ttl, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestTaskStartClaimSchema40MigrationAndLeaseTTLBounds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		ttl  time.Duration
	}{
		{name: "minimum", ttl: workboard.MinLeaseTTL},
		{name: "maximum", ttl: workboard.MaxLeaseTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.Exec(`DROP TRIGGER workboard_auxiliary_review_settlement_immutable_delete;
				DROP TRIGGER workboard_auxiliary_review_settlement_immutable_update;
				DROP TRIGGER workboard_auxiliary_review_settlement_binding;
				DROP INDEX workboard_auxiliary_review_settlements_board;
				DROP TABLE workboard_auxiliary_review_settlements;
				DROP TRIGGER workboard_auxiliary_review_admission_immutable_delete;
				DROP TRIGGER workboard_auxiliary_review_admission_immutable_update;
				DROP TRIGGER workboard_auxiliary_review_admission_binding;
				DROP INDEX workboard_auxiliary_review_admissions_board;
				DROP TABLE workboard_auxiliary_review_admissions;
				DROP TRIGGER workboard_execution_settlement_immutable_delete;
				DROP TRIGGER workboard_execution_settlement_immutable_update;
				DROP TRIGGER workboard_execution_admission_immutable_delete;
				DROP TRIGGER workboard_execution_admission_immutable_update;
				DROP TRIGGER workboard_execution_settlement_binding;
				DROP TRIGGER workboard_execution_admission_binding;
				DROP TRIGGER workboard_execution_admission_no_active;
				DROP INDEX workboard_execution_settlements_board;
				DROP INDEX workboard_execution_admissions_card;
				DROP INDEX workboard_execution_admissions_board;
				DROP INDEX workboard_execution_admissions_global;
				DROP TABLE workboard_execution_settlements;
				DROP TABLE workboard_execution_admissions;
				DROP TRIGGER workboard_task_start_claim_immutable_delete;
				DROP TRIGGER workboard_task_start_claim_immutable_update;
				DROP INDEX workboard_task_start_claims_board;
				DROP TABLE workboard_task_start_claims;
				PRAGMA user_version=40;`); err != nil {
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
			var migrated, markers int
			if err = store.db.QueryRow(`PRAGMA user_version`).Scan(&migrated); err != nil || migrated != currentStorageSchema {
				t.Fatalf("schema=%d err=%v", migrated, err)
			}
			if err = store.db.QueryRow(`SELECT count(*) FROM workboard_task_start_claims`).Scan(&markers); err != nil || markers != 0 {
				t.Fatalf("migration invented markers=%d err=%v", markers, err)
			}

			clock := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
			card, boardID := readyLifecycleCard(t, ctx, store, clock)
			clock = card.UpdatedAt.Add(time.Second)
			event := atomicStart("ttl-worker", "ttl-task", "ttl-session", clock)
			request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "ttl-boundary-claim", ExpectedCardRevision: card.Revision,
				TaskID: event.TaskID, SessionID: event.SessionID}
			if _, err = atomicClaimServiceWithTTL(t, store, event.WorkerID, &clock, tc.ttl).ClaimTaskStart(ctx, event, request); err != nil {
				t.Fatal(err)
			}
			var storedTTL int64
			if err = store.db.QueryRow(`SELECT lease_ttl_ns FROM workboard_task_start_claims WHERE task_id=?`, event.TaskID).Scan(&storedTTL); err != nil || storedTTL != int64(tc.ttl) {
				t.Fatalf("stored ttl=%d want=%d err=%v", storedTTL, tc.ttl, err)
			}
		})
	}
}

func TestTaskStartClaimMigrationRejectsRetainedFutureSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`PRAGMA user_version=40`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := Open(ctx, path); openErr == nil {
		reopened.Close()
		t.Fatal("schema-40 database retained an unprovable schema-41 table")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, retained int
	if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 40 {
		t.Fatalf("failed migration changed version=%d err=%v", version, err)
	}
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workboard_task_start_claims'`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("failed migration changed retained table=%d err=%v", retained, err)
	}
}

func TestTaskStartClaimAtomicRestartAndExactTwoHalfReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	clock = card.UpdatedAt.Add(time.Second)
	event := atomicStart("atomic-worker", "atomic-task", "atomic-session", clock)
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "atomic-start-claim-01", ExpectedCardRevision: card.Revision,
		TaskID: event.TaskID, SessionID: event.SessionID}
	receipt, err := atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request)
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Read(ctx, event.TaskID, 0, 10)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
	attempt := snapshots[card.ID].Attempt
	if err != nil || snapshotErr != nil || len(items) != 1 || !reflect.DeepEqual(items[0], event) || attempt == nil || attempt.Claim == nil ||
		attempt.WorkerID != event.WorkerID || attempt.Claim.TaskID != event.TaskID || !reflect.DeepEqual(attempt.SessionIDs, []string{event.SessionID}) {
		t.Fatalf("events=%+v attempt=%+v errors=%v/%v", items, attempt, err, snapshotErr)
	}
	var markers, timings, logEvents int
	if store.db.QueryRow(`SELECT count(*) FROM workboard_task_start_claims WHERE task_id=?`, event.TaskID).Scan(&markers) != nil ||
		store.db.QueryRow(`SELECT count(*) FROM task_timings WHERE task_id=?`, event.TaskID).Scan(&timings) != nil ||
		store.db.QueryRow(`SELECT count(*) FROM event_log WHERE event_id=?`, event.ID).Scan(&logEvents) != nil || markers != 1 || timings != 1 || logEvents != 1 {
		t.Fatalf("markers=%d timings=%d log=%d", markers, timings, logEvents)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replayed, err := atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	var original []byte
	if err = store.db.QueryRow(`SELECT body FROM events WHERE id=?`, event.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE events SET body=json_set(body,'$.data.text','forged') WHERE id=?`, event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("runtime-half tamper accepted: %v", err)
	}
	if _, err = store.db.Exec(`UPDATE events SET body=? WHERE id=?`, original, event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_task_start_claim_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_task_start_claims SET worker_id='forged-worker' WHERE task_id=?`, event.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("claim-boundary tamper accepted: %v", err)
	}
}

func TestTaskStartClaimRejectsPreexistingAndUnmarkedHalves(t *testing.T) {
	for _, withClaim := range []bool{false, true} {
		t.Run(map[bool]string{false: "runtime_only", true: "independently_committed_halves"}[withClaim], func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			clock := time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC)
			card, boardID := readyLifecycleCard(t, ctx, store, clock)
			clock = card.UpdatedAt.Add(time.Second)
			event := atomicStart("preexisting-worker", "preexisting-task", "preexisting-session", clock)
			if err = store.Append(ctx, 0, event); err != nil {
				t.Fatal(err)
			}
			request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "preexisting-claim-key", ExpectedCardRevision: card.Revision,
				TaskID: event.TaskID, SessionID: event.SessionID}
			service := atomicClaimService(t, store, event.WorkerID, &clock)
			if withClaim {
				if _, err = service.Claim(ctx, request); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = service.ClaimTaskStart(ctx, event, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("pre-existing halves adopted: %v", err)
			}
			var markers int
			if err = store.db.QueryRow(`SELECT count(*) FROM workboard_task_start_claims`).Scan(&markers); err != nil || markers != 0 {
				t.Fatalf("markers=%d err=%v", markers, err)
			}
		})
	}
}

func TestTaskStartClaimRollbackStaleIdentityAndRetry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	clock = card.UpdatedAt.Add(time.Second)
	event := atomicStart("rollback-worker", "rollback-task", "rollback-session", clock)
	base := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "rollback-atomic-key", ExpectedCardRevision: card.Revision,
		TaskID: event.TaskID, SessionID: event.SessionID}
	service := atomicClaimService(t, store, event.WorkerID, &clock)
	stale := base
	stale.ExpectedCardRevision--
	if _, err = service.ClaimTaskStart(ctx, event, stale); err == nil {
		t.Fatal("stale claim succeeded")
	}
	for name, mutate := range map[string]func(*runtime.Event, *workboard.ClaimRequest){
		"worker":  func(e *runtime.Event, _ *workboard.ClaimRequest) { e.WorkerID = "other-worker" },
		"task":    func(_ *runtime.Event, r *workboard.ClaimRequest) { r.TaskID = "other-task" },
		"session": func(_ *runtime.Event, r *workboard.ClaimRequest) { r.SessionID = "other-session" },
	} {
		t.Run(name, func(t *testing.T) {
			changedEvent, changedRequest := event, base
			mutate(&changedEvent, &changedRequest)
			if _, callErr := service.ClaimTaskStart(ctx, changedEvent, changedRequest); callErr == nil {
				t.Fatal("identity mismatch succeeded")
			}
		})
	}
	if _, err = store.db.Exec(`CREATE TRIGGER reject_atomic_attempt BEFORE INSERT ON workboard_attempts BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ClaimTaskStart(ctx, event, base); err == nil {
		t.Fatal("injected claim failure succeeded")
	}
	for _, table := range []string{"task_heads", "events", "event_log", "task_timings", "workboard_attempts", "workboard_claims", "workboard_task_start_claims"} {
		var count int
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE 1=1`).Scan(&count); queryErr != nil {
			t.Fatal(table, queryErr)
		}
		if (table == "task_heads" || table == "events" || table == "event_log" || table == "task_timings") && count != 0 {
			t.Fatalf("partial runtime state in %s: %d", table, count)
		}
		if strings.HasPrefix(table, "workboard_attempt") || table == "workboard_claims" || table == "workboard_task_start_claims" {
			if count != 0 {
				t.Fatalf("partial claim state in %s: %d", table, count)
			}
		}
	}
	if _, err = store.db.Exec(`DROP TRIGGER reject_atomic_attempt`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ClaimTaskStart(ctx, event, base); err != nil {
		t.Fatal("retry after rollback:", err)
	}
}

func TestTaskStartClaimContentionHasOneOwnerAndNoLoserTask(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	clock := time.Date(2026, 9, 10, 15, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, first, clock)
	clock = card.UpdatedAt.Add(time.Second)
	type result struct {
		task string
		err  error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{first, second} {
		worker, task := "race-worker-"+string(rune('a'+i)), "race-task-"+string(rune('a'+i))
		event := atomicStart(worker, task, "race-session-"+string(rune('a'+i)), clock)
		request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "race-atomic-key-" + string(rune('a'+i)),
			ExpectedCardRevision: card.Revision, TaskID: event.TaskID, SessionID: event.SessionID}
		service := atomicClaimService(t, store, worker, &clock)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := service.ClaimTaskStart(ctx, event, request)
			results <- result{task, callErr}
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for item := range results {
		var count int
		if item.err == nil {
			winners++
			if first.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, item.task).Scan(&count) != nil || count != 1 {
				t.Fatalf("winner task missing: %s", item.task)
			}
		} else if first.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, item.task).Scan(&count) != nil || count != 0 {
			t.Fatalf("loser task persisted: %s err=%v", item.task, item.err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestTaskStartClaimPreservesStartProjectionGates(t *testing.T) {
	for _, gate := range []string{"event_log", "timing", "skill", "compaction", "final_marker"} {
		t.Run(gate, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			clock := time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC)
			card, boardID := readyLifecycleCard(t, ctx, store, clock)
			clock = card.UpdatedAt.Add(time.Second)
			event := atomicStart("gate-worker", "gate-task", "gate-session", clock)
			switch gate {
			case "event_log":
				_, err = store.db.Exec(`CREATE TRIGGER reject_atomic_event_log BEFORE INSERT ON event_log BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			case "timing":
				_, err = store.db.Exec(`CREATE TRIGGER reject_atomic_timing BEFORE INSERT ON task_timings BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			case "skill":
				event.Data.Privacy = "local_only"
				event.Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "skill", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
				_, err = store.db.Exec(`CREATE TRIGGER reject_atomic_skill BEFORE INSERT ON skill_exposures BEGIN SELECT RAISE(ABORT,'fixture'); END`)
			case "compaction":
				event.Data.ParentTaskID = "missing-parent"
				event.Data.Compaction = &runtime.ContextCompaction{Version: 1, SummaryAttemptID: "missing-attempt", SummaryReviewID: "missing-review",
					SourceTaskID: "missing-parent", SourceSequence: 1, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1,
					Summary: runtime.ContextSummary{PendingWork: []string{"continue"}}}
			case "final_marker":
				_, err = store.db.Exec(`CREATE TRIGGER reject_atomic_final_marker BEFORE INSERT ON workboard_task_start_claims BEGIN SELECT RAISE(ABORT,'atomic final marker failure'); END`)
			}
			if err != nil {
				t.Fatal(err)
			}
			request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "gate-atomic-claim-1", ExpectedCardRevision: card.Revision,
				TaskID: event.TaskID, SessionID: event.SessionID}
			if _, err = atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request); err == nil {
				t.Fatal("projection gate was bypassed")
			}
			for _, table := range []string{"task_heads", "events", "workboard_attempts", "workboard_task_start_claims"} {
				var count int
				if queryErr := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); queryErr != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, queryErr)
				}
			}
		})
	}
}

func TestTaskStartClaimPreservesNonConflictInsertFailures(t *testing.T) {
	for _, table := range []string{"task_heads", "events"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			clock := time.Date(2026, 9, 10, 17, 0, 0, 0, time.UTC)
			card, boardID := readyLifecycleCard(t, ctx, store, clock)
			clock = card.UpdatedAt.Add(time.Second)
			event := atomicStart("insert-worker", "insert-task", "insert-session", clock)
			message := "atomic " + table + " insert failure"
			if _, err = store.db.Exec(`CREATE TRIGGER reject_atomic_insert BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'` + message + `'); END`); err != nil {
				t.Fatal(err)
			}
			request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "insert-failure-key", ExpectedCardRevision: card.Revision,
				TaskID: event.TaskID, SessionID: event.SessionID}
			_, err = atomicClaimService(t, store, event.WorkerID, &clock).ClaimTaskStart(ctx, event, request)
			if err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), message) {
				t.Fatalf("non-conflict SQLite failure was collapsed: %v", err)
			}
			for _, name := range []string{"task_heads", "events", "event_log", "task_timings", "workboard_attempts", "workboard_claims", "workboard_task_start_claims"} {
				var count int
				if queryErr := store.db.QueryRow(`SELECT count(*) FROM ` + name).Scan(&count); queryErr != nil || count != 0 {
					t.Fatalf("partial state in %s: count=%d err=%v", name, count, queryErr)
				}
			}
		})
	}
}

func TestTaskStartClaimReplaysOnlyCoherentProgressedAndTerminalTasks(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "progressed", true: "terminal"}[terminal], func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			clock := time.Date(2026, 9, 10, 17, 15, 0, 0, time.UTC)
			card, boardID := readyLifecycleCard(t, ctx, store, clock)
			clock = card.UpdatedAt.Add(time.Second)
			event := atomicStart("replay-worker", "replay-task", "replay-session", clock)
			request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "coherent-replay-key", ExpectedCardRevision: card.Revision,
				TaskID: event.TaskID, SessionID: event.SessionID}
			service := atomicClaimService(t, store, event.WorkerID, &clock)
			receipt, err := service.ClaimTaskStart(ctx, event, request)
			if err != nil {
				t.Fatal(err)
			}
			next := runtime.Event{Version: 1, ID: "replay-task-next", TaskID: event.TaskID, SessionID: event.SessionID,
				CorrelationID: event.TaskID, WorkerID: event.WorkerID, Sequence: 2, Time: clock.Add(time.Second),
				Kind: runtime.TurnStarted, TurnID: "replay-turn", AttemptID: "replay-attempt"}
			if terminal {
				next.Kind, next.TurnID, next.AttemptID = runtime.TaskFailed, "", ""
				next.Data.Code = "provider_failed"
			}
			if err = store.Append(ctx, 1, next); err != nil {
				t.Fatal(err)
			}
			replay, err := service.ClaimTaskStart(ctx, event, request)
			if err != nil || !reflect.DeepEqual(replay, receipt) {
				t.Fatalf("healthy replay=%+v err=%v", replay, err)
			}
			if terminal {
				_, err = store.db.Exec(`UPDATE task_timings SET terminal_event_id='forged-terminal' WHERE task_id=?`, event.TaskID)
			} else {
				_, err = store.db.Exec(`UPDATE events SET body=body||' ' WHERE task_id=? AND sequence=2`, event.TaskID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.ClaimTaskStart(ctx, event, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("progressed task tamper accepted: %v", err)
			}
		})
	}
}

func TestTaskStartClaimSameTaskCrossCardContention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	clock := time.Date(2026, 9, 10, 17, 30, 0, 0, time.UTC)
	firstCard, boardID := readyLifecycleCard(t, ctx, first, clock)
	operator := workboard.Actor{ID: "operator", Type: "operator"}
	cards, err := workboard.NewCardService(first, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: operator}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := first.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	created, err := cards.CreateCard(ctx, workboard.CreateCardRequest{BoardID: boardID, IdempotencyKey: "cross-card-create", ExpectedBoardRevision: snapshot.Board.Revision,
		ExpectedGraphRevision: snapshot.GraphRevision, Card: workboard.NewCard{Title: "Second", Priority: "normal", Labels: []string{}, Dependencies: []string{},
			Budget: workboardTestBudget(), Criteria: workboardTestCriteria()}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = first.ReadWorkboard(ctx, boardID, workboard.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	secondCard, err := cards.MoveCard(ctx, workboard.MoveCardRequest{BoardID: boardID, CardID: created.ID, IdempotencyKey: "cross-card-ready",
		TargetState: workboard.Ready, ExpectedBoardRevision: snapshot.Board.Revision, ExpectedCardRevision: created.Revision,
		ExpectedLayoutRevision: snapshot.Board.LayoutRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = secondCard.Card.UpdatedAt.Add(time.Second)
	event := atomicStart("cross-card-worker", "cross-card-task", "cross-card-session", clock)
	type result struct {
		card string
		err  error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, fixture := range []struct {
		store *Store
		card  workboard.Card
	}{{first, firstCard}, {second, secondCard.Card}} {
		request := workboard.ClaimRequest{BoardID: boardID, CardID: fixture.card.ID, IdempotencyKey: "cross-card-claim-" + string(rune('a'+i)),
			ExpectedCardRevision: fixture.card.Revision, TaskID: event.TaskID, SessionID: event.SessionID}
		service := atomicClaimService(t, fixture.store, event.WorkerID, &clock)
		wg.Add(1)
		go func(card string) {
			defer wg.Done()
			_, callErr := service.ClaimTaskStart(ctx, event, request)
			results <- result{card: card, err: callErr}
		}(fixture.card.ID)
	}
	wg.Wait()
	close(results)
	winners := 0
	for outcome := range results {
		if outcome.err == nil {
			winners++
		} else if !errors.Is(outcome.err, ErrConflict) {
			t.Fatalf("unexpected loser error for %s: %v", outcome.card, outcome.err)
		}
	}
	var heads, markers, claims int
	if first.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id=?`, event.TaskID).Scan(&heads) != nil ||
		first.db.QueryRow(`SELECT count(*) FROM workboard_task_start_claims WHERE task_id=?`, event.TaskID).Scan(&markers) != nil ||
		first.db.QueryRow(`SELECT count(*) FROM workboard_claims WHERE task_id=?`, event.TaskID).Scan(&claims) != nil ||
		winners != 1 || heads != 1 || markers != 1 || claims != 1 {
		t.Fatalf("winners=%d heads=%d markers=%d claims=%d", winners, heads, markers, claims)
	}
}
