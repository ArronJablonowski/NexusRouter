package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestWorkboardReassignmentReadersExposeExactLineage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Now().UTC().Add(-time.Minute)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "reader-replacement", Type: "worker"},
		verifiedLifecycleRecovery("reader-unused-proof", workboard.EffectFree), &clock)
	if _, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "reader-replacement-key", ExpectedCardRevision: fixture.recoveredCardRevision}); err != nil {
		t.Fatal(err)
	}
	successorAttemptID, successorClaimID := currentLifecycleIDs(t, store, fixture.boardID, fixture.cardID)
	want := workboard.ReassignmentRecord{Version: 1, RecoveryID: fixture.recoveryID, BoardID: fixture.boardID, CardID: fixture.cardID,
		PredecessorAttemptID: fixture.predecessorAttemptID, PredecessorClaimID: fixture.predecessorClaimID,
		SuccessorAttemptID: successorAttemptID, SuccessorClaimID: successorClaimID, CreatedAt: clock}

	snapshots, err := store.ReadCardLifecycleSnapshots(ctx, fixture.boardID, []string{fixture.cardID})
	if err != nil || snapshots[fixture.cardID].Attempt == nil || snapshots[fixture.cardID].Attempt.Reassignment == nil ||
		!reflect.DeepEqual(*snapshots[fixture.cardID].Attempt.Reassignment, want) {
		t.Fatalf("snapshot lineage=%+v err=%v", snapshots[fixture.cardID], err)
	}
	detail, err := store.ReadAttemptDetail(ctx, fixture.boardID, fixture.cardID, successorAttemptID,
		workboard.AttemptDetailOptions{Limit: 10})
	if err != nil || detail.Attempt.Reassignment == nil || !reflect.DeepEqual(*detail.Attempt.Reassignment, want) {
		t.Fatalf("detail lineage=%+v err=%v", detail.Attempt.Reassignment, err)
	}
	history, err := store.ListAttemptHistory(ctx, fixture.boardID, fixture.cardID, workboard.AttemptHistoryOptions{Limit: 10})
	if err != nil || len(history.Items) != 2 || history.Items[0].Reassignment == nil ||
		!reflect.DeepEqual(*history.Items[0].Reassignment, want) || history.Items[1].Reassignment != nil {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestWorkboardReassignmentReaderRejectsCanonicalDrift(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Now().UTC().Add(-time.Minute)
	fixture := recoverLifecycleClaim(t, ctx, store, &clock, 3)
	replacement := newTestLifecycleService(t, store, workboard.Actor{ID: "drift-replacement", Type: "worker"},
		verifiedLifecycleRecovery("drift-unused-proof", workboard.EffectFree), &clock)
	if _, err = replacement.Claim(ctx, workboard.ClaimRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "drift-replacement-key", ExpectedCardRevision: fixture.recoveredCardRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`DROP TRIGGER workboard_reassignment_immutable_update;
		UPDATE workboard_reassignments SET body=json_set(body,'$.successor_claim_id','forged-claim') WHERE recovery_id=?`, fixture.recoveryID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadCardLifecycleSnapshots(ctx, fixture.boardID, []string{fixture.cardID}); !errors.Is(err, ErrWorkboardCorrupt) {
		t.Fatalf("canonical drift error=%v", err)
	}
}
