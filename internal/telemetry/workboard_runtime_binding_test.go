package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestBoundWorkboardClaimIsAtomicRestartSafeAndReplayChecked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "bound-task-start", TaskID: "bound-task", SessionID: "bound-session",
		CorrelationID: "bound-task", Sequence: 1, Time: now.Add(time.Second), Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	clock := card.UpdatedAt.Add(time.Second)
	service := newTestLifecycleService(t, store, workboard.Actor{ID: "bound-worker", Type: "worker"}, verifiedLifecycleRecovery("proof", workboard.EffectFree), &clock)
	request := workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "bound-claim-operation", ExpectedCardRevision: card.Revision,
		TaskID: "bound-task", SessionID: "bound-session"}
	receipt, err := service.Claim(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{card.ID})
	attempt := snapshots[card.ID].Attempt
	if err != nil || attempt == nil || attempt.Claim == nil || attempt.Claim.TaskID != request.TaskID ||
		!reflect.DeepEqual(attempt.TaskIDs, []string{request.TaskID}) || !reflect.DeepEqual(attempt.SessionIDs, []string{request.SessionID}) {
		t.Fatalf("snapshot=%+v err=%v", snapshots, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock = clock.Add(time.Minute)
	service = newTestLifecycleService(t, store, workboard.Actor{ID: "bound-worker", Type: "worker"}, verifiedLifecycleRecovery("proof", workboard.EffectFree), &clock)
	replayed, err := service.Claim(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	var originalBody []byte
	if err = store.db.QueryRow(`SELECT body FROM workboard_attempts WHERE id=?`, attempt.ID).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	tamperCases := []struct {
		name    string
		tamper  string
		args    []any
		restore string
		back    []any
	}{
		{name: "normalized worker", tamper: `UPDATE workboard_attempts SET worker_id='forged-worker' WHERE id=?`, args: []any{attempt.ID},
			restore: `UPDATE workboard_attempts SET worker_id='bound-worker' WHERE id=?`, back: []any{attempt.ID}},
		{name: "normalized budget", tamper: `UPDATE workboard_attempts SET token_limit=token_limit+1 WHERE id=?`, args: []any{attempt.ID},
			restore: `UPDATE workboard_attempts SET token_limit=token_limit-1 WHERE id=?`, back: []any{attempt.ID}},
		{name: "canonical body", tamper: `UPDATE workboard_attempts SET body=json_set(body,'$.policy_digest',?) WHERE id=?`, args: []any{strings.Repeat("f", 64), attempt.ID},
			restore: `UPDATE workboard_attempts SET body=? WHERE id=?`, back: []any{originalBody, attempt.ID}},
		{name: "normalized claim", tamper: `UPDATE workboard_claims SET expires_at=expires_at+1 WHERE id=?`, args: []any{attempt.Claim.ID},
			restore: `UPDATE workboard_claims SET expires_at=expires_at-1 WHERE id=?`, back: []any{attempt.Claim.ID}},
	}
	for _, test := range tamperCases {
		t.Run(test.name, func(t *testing.T) {
			if _, execErr := store.db.Exec(test.tamper, test.args...); execErr != nil {
				t.Fatal(execErr)
			}
			if _, replayErr := service.Claim(ctx, request); !errors.Is(replayErr, ErrWorkboardCorrupt) {
				t.Fatalf("tampered exact replay error=%v", replayErr)
			}
			if _, execErr := store.db.Exec(test.restore, test.back...); execErr != nil {
				t.Fatal(execErr)
			}
		})
	}
	if _, err = store.db.Exec(`UPDATE workboard_attempt_sessions SET session_id='other-session' WHERE attempt_id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Claim(ctx, request); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("tampered association replay error=%v", err)
	}
}

func TestBoundWorkboardClaimRollsBackWithoutDurableRuntimeTask(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := card.UpdatedAt.Add(time.Second)
	service := newTestLifecycleService(t, store, workboard.Actor{ID: "bound-worker", Type: "worker"}, verifiedLifecycleRecovery("proof", workboard.EffectFree), &clock)
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "wrong-session-start", TaskID: "wrong-session-task", SessionID: "actual-session",
		CorrelationID: "wrong-session-task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "wrong-session-claim1",
		ExpectedCardRevision: card.Revision, TaskID: "wrong-session-task", SessionID: "claimed-session"}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("mismatched session error=%v", err)
	}
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "terminal-task-start", TaskID: "terminal-task", SessionID: "terminal-session",
		CorrelationID: "terminal-task", Sequence: 1, Time: now, Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(ctx, 1, runtime.Event{Version: 1, ID: "terminal-task-done", TaskID: "terminal-task", SessionID: "terminal-session",
		CorrelationID: "terminal-task", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "terminal-task-claim1",
		ExpectedCardRevision: card.Revision, TaskID: "terminal-task", SessionID: "terminal-session"}); !errors.Is(err, &workboard.Violation{Code: workboard.CodeIllegalTransition}) {
		t.Fatalf("terminal task binding error=%v", err)
	}
	_, err = service.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "missing-task-claim-01",
		ExpectedCardRevision: card.Revision, TaskID: "missing-task", SessionID: "missing-session"})
	if err == nil {
		t.Fatal("claim without a durable runtime task succeeded")
	}
	for _, table := range []string{"workboard_attempts", "workboard_claims", "workboard_attempt_tasks", "workboard_attempt_sessions"} {
		var count int
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); queryErr != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, queryErr)
		}
	}
	var state string
	if err = store.db.QueryRow(`SELECT state FROM workboard_cards WHERE board_id=? AND id=?`, boardID, card.ID).Scan(&state); err != nil || state != string(workboard.Ready) {
		t.Fatalf("card state=%q err=%v", state, err)
	}
}

func TestLifecycleServiceRejectsPartialRuntimeBinding(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	clock := now.Add(time.Second)
	service := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifiedLifecycleRecovery("proof", workboard.EffectFree), &clock)
	for _, request := range []workboard.ClaimRequest{
		{BoardID: boardID, CardID: card.ID, IdempotencyKey: strings.Repeat("a", 16), ExpectedCardRevision: card.Revision, TaskID: "task"},
		{BoardID: boardID, CardID: card.ID, IdempotencyKey: strings.Repeat("b", 16), ExpectedCardRevision: card.Revision, SessionID: "session"},
	} {
		if _, err = service.Claim(ctx, request); !errors.Is(err, &workboard.Violation{Code: workboard.CodeInvalid}) {
			t.Fatalf("partial binding accepted: %+v err=%v", request, err)
		}
	}
}
