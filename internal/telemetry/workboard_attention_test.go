package telemetry

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestWorkboardAttentionScanIsDurableIdempotentAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seed := time.Date(2026, 9, 9, 22, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, seed)
	clock := card.UpdatedAt.Add(time.Second)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifiedLifecycleRecovery("attention-proof", workboard.EffectFree), &clock)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "attention-seed-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(40 * time.Second)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workboard.NewAttentionService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "supervisor", Type: "system"}}}, func() time.Time { return clock }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := service.Scan(ctx, boardID, 10)
	if err != nil || len(observed) != 1 || observed[0].Validate() != nil || observed[0].Reason != workboard.AttentionStale ||
		observed[0].ClaimID != claimID || observed[0].OwnerID != "worker-a" || observed[0].ClaimRevision != 2 {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	var claimState, ownerID string
	var released any
	if err = store.db.QueryRow(`SELECT state,owner_id,released_at FROM workboard_claims WHERE id=?`, claimID).Scan(&claimState, &ownerID, &released); err != nil ||
		claimState != string(workboard.LeaseAttention) || ownerID != "worker-a" || released != nil {
		t.Fatalf("claim state=%q owner=%q released=%v err=%v", claimState, ownerID, released, err)
	}
	var attemptRevision, boardSequence int64
	if err = store.db.QueryRow(`SELECT revision FROM workboard_attempts WHERE id=?`, attemptID).Scan(&attemptRevision); err != nil || attemptRevision != 2 {
		t.Fatalf("attempt revision=%d err=%v", attemptRevision, err)
	}
	if err = store.db.QueryRow(`SELECT event_sequence FROM workboard_boards WHERE id=?`, boardID).Scan(&boardSequence); err != nil {
		t.Fatal(err)
	}
	var events, receipts, activeClaims int
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_events WHERE board_id=? AND kind='claim.attention' AND actor_type='system'`, boardID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM workboard_operations o JOIN workboard_events e ON e.board_id=o.board_id AND e.operation_id=o.operation_id WHERE e.board_id=? AND e.kind='claim.attention'`, boardID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT active_claims FROM workboard_boards WHERE id=?`, boardID).Scan(&activeClaims); err != nil || events != 1 || receipts != 1 || activeClaims != 1 {
		t.Fatalf("durability events=%d receipts=%d active_claims=%d err=%v", events, receipts, activeClaims, err)
	}
	again, err := service.Scan(ctx, boardID, 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("repeated scan=%+v err=%v", again, err)
	}
	var repeatedSequence int64
	if err = store.db.QueryRow(`SELECT event_sequence FROM workboard_boards WHERE id=?`, boardID).Scan(&repeatedSequence); err != nil || repeatedSequence != boardSequence {
		t.Fatalf("idempotent sequence=%d want=%d err=%v", repeatedSequence, boardSequence, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reader, err := workboard.NewAttentionService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "operator", Type: "operator"}}}, func() time.Time { return clock }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := reader.List(ctx, boardID, 10)
	if err != nil || len(items) != 1 || items[0] != observed[0] {
		t.Fatalf("restart list=%+v want=%+v err=%v", items, observed, err)
	}
}

func TestWorkboardAttentionScanRollbackAndConcurrentIdempotence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seed := time.Date(2026, 9, 9, 22, 30, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, seed)
	clock := card.UpdatedAt.Add(time.Second)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifiedLifecycleRecovery("attention-race-proof", workboard.EffectFree), &clock)
	if _, err = worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "attention-race-claim", ExpectedCardRevision: card.Revision}); err != nil {
		t.Fatal(err)
	}
	_, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	clock = clock.Add(2 * time.Minute)
	service, err := workboard.NewAttentionService(store, telemetryCardAuthority{authority: workboard.Authority{CreationScope: "session", Actor: workboard.Actor{ID: "supervisor", Type: "system"}}}, func() time.Time { return clock }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_attention_event BEFORE INSERT ON workboard_events WHEN NEW.kind='claim.attention' BEGIN SELECT RAISE(ABORT,'attention event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Scan(ctx, boardID, 10); err == nil {
		t.Fatal("attention event failure did not roll back")
	}
	var state string
	var revision int64
	if err = store.db.QueryRow(`SELECT state,revision FROM workboard_claims WHERE id=?`, claimID).Scan(&state, &revision); err != nil || state != "active" || revision != 1 {
		t.Fatalf("rolled back claim state=%q revision=%d err=%v", state, revision, err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER fail_attention_event`); err != nil {
		t.Fatal(err)
	}
	const callers = 8
	results := make(chan int, callers)
	errorsOut := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			items, scanErr := service.Scan(ctx, boardID, 10)
			results <- len(items)
			errorsOut <- scanErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsOut)
	total := 0
	for itemCount := range results {
		total += itemCount
	}
	for scanErr := range errorsOut {
		if scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	if total != 1 {
		t.Fatalf("concurrent observations=%d", total)
	}
}
