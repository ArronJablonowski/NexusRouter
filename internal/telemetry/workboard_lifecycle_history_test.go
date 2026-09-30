package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/workboard"
)

func TestLifecycleHistoryPagesFreezeHighWaterAndRestartFresh(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	clock = card.UpdatedAt
	worker := workboard.Actor{ID: "worker-a", Type: "worker"}
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("history-proof-a", workboard.EffectFree), &clock)
	claimAttempt := func(key string) (string, string) {
		current, readErr := store.GetCard(ctx, boardID, card.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		clock = clock.Add(time.Second)
		if _, claimErr := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: key, ExpectedCardRevision: current.Revision}); claimErr != nil {
			t.Fatal(key, claimErr)
		}
		return currentLifecycleIDs(t, store, boardID, card.ID)
	}
	recoverAttempt := func(attemptID, claimID, key, proofID string) {
		current, readErr := store.GetCard(ctx, boardID, card.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var claimRevision int64
		if readErr = store.db.QueryRow(`SELECT revision FROM workboard_claims WHERE id=?`, claimID).Scan(&claimRevision); readErr != nil {
			t.Fatal(readErr)
		}
		clock = clock.Add(2 * time.Minute)
		verifier := verifiedLifecycleRecovery(proofID, workboard.EffectFree)
		supervisor := newTestLifecycleService(t, store, workboard.Actor{ID: "supervisor", Type: "system"}, verifier, &clock)
		if _, recoverErr := supervisor.Recover(ctx, workboard.RecoverClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID,
			ClaimID: claimID, IdempotencyKey: key, ExpectedCardRevision: current.Revision, ExpectedClaimRevision: claimRevision,
			Proof: recoveryIntent(verifier.proof)}); recoverErr != nil {
			t.Fatal(recoverErr)
		}
	}
	firstAttempt, firstClaim := claimAttempt("history-claim-one")
	recoverAttempt(firstAttempt, firstClaim, "history-recover-one", "history-proof-one")
	secondAttempt, secondClaim := claimAttempt("history-claim-two")
	progress := newTestProgressService(t, store, worker, &clock)
	for index, evidence := range []string{"checkpoint one", "checkpoint two"} {
		current, readErr := store.GetCard(ctx, boardID, card.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		clock = clock.Add(time.Second)
		if _, appendErr := progress.AppendCheckpoint(ctx, workboard.AppendCheckpointRequest{BoardID: boardID, CardID: card.ID, AttemptID: secondAttempt,
			ClaimID: secondClaim, IdempotencyKey: "history-checkpoint-" + string(rune('a'+index)), ExpectedCardRevision: current.Revision,
			ExpectedClaimRevision: 1, CriteriaRevision: current.CriteriaRevision, Evidence: evidence}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	detailFirst, err := store.ReadAttemptDetail(ctx, boardID, card.ID, secondAttempt, workboard.AttemptDetailOptions{Limit: 1})
	if err != nil || detailFirst.Validate() != nil || detailFirst.CheckpointHighWaterRevision != 2 || len(detailFirst.Checkpoints) != 1 ||
		detailFirst.Checkpoints[0].Revision != 2 || !detailFirst.HasMore {
		t.Fatalf("first detail=%+v err=%v", detailFirst, err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_checkpoints SET evidence='forged' WHERE attempt_id=? AND revision=2`, secondAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadAttemptDetail(ctx, boardID, card.ID, secondAttempt, workboard.AttemptDetailOptions{Limit: 1}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("normalized checkpoint tamper accepted: %v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_checkpoints SET evidence='checkpoint two' WHERE attempt_id=? AND revision=2`, secondAttempt); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCard(ctx, boardID, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if _, err = progress.AppendCheckpoint(ctx, workboard.AppendCheckpointRequest{BoardID: boardID, CardID: card.ID, AttemptID: secondAttempt,
		ClaimID: secondClaim, IdempotencyKey: "history-checkpoint-c", ExpectedCardRevision: current.Revision,
		ExpectedClaimRevision: 1, CriteriaRevision: current.CriteriaRevision, Evidence: "checkpoint three"}); err != nil {
		t.Fatal(err)
	}
	detailSecond, err := store.ReadAttemptDetail(ctx, boardID, card.ID, secondAttempt, workboard.AttemptDetailOptions{After: detailFirst.NextCursor, Limit: 1})
	if err != nil || detailSecond.Validate() != nil || detailSecond.CheckpointHighWaterRevision != 2 || len(detailSecond.Checkpoints) != 1 ||
		detailSecond.Checkpoints[0].Revision != 1 || detailSecond.HasMore {
		t.Fatalf("second detail=%+v err=%v", detailSecond, err)
	}
	historyFirst, err := store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{Limit: 1})
	if err != nil || historyFirst.Validate() != nil || historyFirst.HighWaterOrdinal != 2 || len(historyFirst.Items) != 1 || historyFirst.Items[0].Ordinal != 2 || !historyFirst.HasMore {
		t.Fatalf("first history=%+v err=%v", historyFirst, err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_attempts SET worker_id='forged-worker' WHERE id=?`, secondAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{Limit: 1}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("normalized attempt tamper accepted: %v", err)
	}
	if _, err = store.db.Exec(`UPDATE workboard_attempts SET worker_id='worker-a' WHERE id=?`, secondAttempt); err != nil {
		t.Fatal(err)
	}
	recoverAttempt(secondAttempt, secondClaim, "history-recover-two", "history-proof-two")
	thirdAttempt, _ := claimAttempt("history-claim-three")
	historySecond, err := store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{After: historyFirst.NextCursor, Limit: 1})
	if err != nil || historySecond.Validate() != nil || historySecond.HighWaterOrdinal != 2 || len(historySecond.Items) != 1 || historySecond.Items[0].ID != firstAttempt || historySecond.HasMore {
		t.Fatalf("second history=%+v err=%v", historySecond, err)
	}
	oldHistoryCursor, oldDetailCursor := historyFirst.NextCursor, detailFirst.NextCursor
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{After: oldHistoryCursor, Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("old history cursor survived restart: %v", err)
	}
	if _, err = store.ReadAttemptDetail(ctx, boardID, card.ID, secondAttempt, workboard.AttemptDetailOptions{After: oldDetailCursor, Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("old detail cursor survived restart: %v", err)
	}
	fresh, err := store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{Limit: 3})
	if err != nil || fresh.Validate() != nil || fresh.HighWaterOrdinal != 3 || len(fresh.Items) != 3 || fresh.Items[0].ID != thirdAttempt {
		t.Fatalf("fresh restart history=%+v err=%v", fresh, err)
	}
}

func TestLifecycleHistoryCursorsRejectForgeryAndCrossScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	clock = card.UpdatedAt
	lifecycle := newTestLifecycleService(t, store, workboard.Actor{ID: "worker-a", Type: "worker"}, verifiedLifecycleRecovery("cursor-proof", workboard.EffectFree), &clock)
	for index := 0; index < 2; index++ {
		current, _ := store.GetCard(ctx, boardID, card.ID)
		clock = clock.Add(time.Second)
		if _, err = lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID, IdempotencyKey: "cursor-claim-key-" + string(rune('a'+index)), ExpectedCardRevision: current.Revision}); err != nil {
			t.Fatal(index, err)
		}
		attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
		if index == 0 {
			clock = clock.Add(2 * time.Minute)
			verifier := verifiedLifecycleRecovery("cursor-recovery", workboard.EffectFree)
			supervisor := newTestLifecycleService(t, store, workboard.Actor{ID: "supervisor", Type: "system"}, verifier, &clock)
			current, _ = store.GetCard(ctx, boardID, card.ID)
			if _, err = supervisor.Recover(ctx, workboard.RecoverClaimRequest{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
				IdempotencyKey: "cursor-recover-key", ExpectedCardRevision: current.Revision, ExpectedClaimRevision: 1, Proof: recoveryIntent(verifier.proof)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := store.ListAttemptHistory(ctx, boardID, card.ID, workboard.AttemptHistoryOptions{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	replacement := "x"
	if page.NextCursor[len(page.NextCursor)-1:] == replacement {
		replacement = "y"
	}
	for _, cursor := range []string{page.NextCursor[:len(page.NextCursor)-1] + replacement, page.NextCursor} {
		otherCard := "other-card"
		if cursor != page.NextCursor {
			otherCard = card.ID
		}
		if _, readErr := store.ListAttemptHistory(ctx, boardID, otherCard, workboard.AttemptHistoryOptions{After: cursor, Limit: 1}); !errors.Is(readErr, ErrWorkboardCursor) {
			t.Fatalf("cursor accepted: %v", readErr)
		}
	}
	detailCursor, err := store.encodeAttemptDetailCursor(attemptDetailCursor{Version: 1, BoardDigest: digestBytes([]byte(boardID)), CardDigest: digestBytes([]byte(card.ID)),
		AttemptDigest: digestBytes([]byte("attempt-one")), AttemptRevision: 1, High: 1, Before: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadAttemptDetail(ctx, boardID, card.ID, "attempt-two", workboard.AttemptDetailOptions{After: detailCursor, Limit: 1}); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("cross-attempt detail cursor accepted: %v", err)
	}
	replacement = "x"
	if detailCursor[len(detailCursor)-1:] == replacement {
		replacement = "y"
	}
	if _, err = store.decodeAttemptDetailCursor(detailCursor[:len(detailCursor)-1] + replacement); !errors.Is(err, ErrWorkboardCursor) {
		t.Fatalf("forged detail cursor accepted: %v", err)
	}
}
