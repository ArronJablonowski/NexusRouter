package telemetry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
)

func TestReadWorkerControlUsesExactFencesAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, clock)
	worker := workboard.Actor{ID: "worker-control-reader", Type: "worker"}
	clock = card.UpdatedAt.Add(time.Second)
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "worker-control-start", TaskID: "worker-control-task",
		SessionID: "worker-control-session", CorrelationID: "worker-control-task", Sequence: 1, Time: clock,
		Kind: runtime.TaskStarted, WorkerID: worker.ID}); err != nil {
		t.Fatal(err)
	}
	lifecycle := newTestLifecycleService(t, store, worker, verifiedLifecycleRecovery("unused-control-proof", workboard.EffectFree), &clock)
	claim, err := lifecycle.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "worker-control-claim", ExpectedCardRevision: card.Revision,
		TaskID: "worker-control-task", SessionID: "worker-control-session"})
	if err != nil || claim.CardRevision == nil || claim.ClaimRevision == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	target := workboard.WorkerControlTarget{BoardID: boardID, CardID: card.ID, AttemptID: attemptID, ClaimID: claimID,
		WorkerID: worker.ID, TaskID: "worker-control-task", CardRevision: *claim.CardRevision, ClaimRevision: *claim.ClaimRevision}
	observed, err := store.ReadWorkerControl(ctx, target)
	if err != nil || observed.Validate(target) != nil || observed.CancelRequested || observed.PauseRequested {
		t.Fatalf("initial observation=%+v err=%v", observed, err)
	}
	operator := workboard.Actor{ID: "worker-control-operator", Type: "operator"}
	control := newTestControlService(t, store, operator, &controlVerifier{}, &clock)
	clock = clock.Add(time.Second)
	requested, err := control.RequestCancel(ctx, workboard.RequestCardControl{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "worker-control-cancel", ExpectedCardRevision: target.CardRevision})
	if err != nil || requested.CardRevision == nil {
		t.Fatalf("request=%+v err=%v", requested, err)
	}
	clock = clock.Add(time.Second)
	heartbeat, err := lifecycle.Heartbeat(ctx, workboard.HeartbeatRequest{BoardID: boardID, CardID: card.ID,
		AttemptID: attemptID, ClaimID: claimID, IdempotencyKey: "worker-control-heartbeat",
		ExpectedClaimRevision: target.ClaimRevision})
	if err != nil || heartbeat.CardRevision == nil || heartbeat.ClaimRevision == nil {
		t.Fatalf("heartbeat=%+v err=%v", heartbeat, err)
	}
	target.CardRevision, target.ClaimRevision = *heartbeat.CardRevision, *heartbeat.ClaimRevision
	observed, err = store.ReadWorkerControl(ctx, target)
	if err != nil || observed.Validate(target) != nil || !observed.CancelRequested || observed.PauseRequested {
		t.Fatalf("cancel observation=%+v err=%v", observed, err)
	}
	stale := target
	stale.ClaimRevision--
	if _, err = store.ReadWorkerControl(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale claim fence accepted: %v", err)
	}
	wrong := target
	wrong.TaskID = "replacement-task"
	if _, err = store.ReadWorkerControl(ctx, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement task binding accepted: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed, err = store.ReadWorkerControl(ctx, target)
	if err != nil || observed.Validate(target) != nil || !observed.CancelRequested {
		t.Fatalf("restart observation=%+v err=%v", observed, err)
	}
}
