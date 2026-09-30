package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func TestWorkboardWorkerSafeBoundaryPausesWithLiveHeartbeatAndResumes(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("8", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, reachBoundary := make(chan struct{}), make(chan struct{})
	boundaryDone, runDone := make(chan error, 1), make(chan error, 1)
	var handle *WorkboardWorkerHandle
	go func() {
		_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "pause-resume-task",
			SessionID: "pause-resume-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, worker *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, worker); err != nil {
					return WorkboardCandidate{}, err
				}
				handle = worker
				close(entered)
				<-reachBoundary
				boundaryErr := worker.SafeBoundary(run)
				boundaryDone <- boundaryErr
				if boundaryErr != nil {
					return WorkboardCandidate{}, boundaryErr
				}
				if checkpointErr := worker.AppendCheckpoint(run, "continued after acknowledged resume"); checkpointErr != nil {
					return WorkboardCandidate{}, checkpointErr
				}
				return WorkboardCandidate{Summary: "resumed candidate"}, nil
			}, Validate: func(context.Context, WorkboardCandidate) error { return nil },
		})
		runDone <- runErr
	}()
	waitSignal(t, entered, "worker callback")
	requestWorkerPause(t, ctx, bridge, store, boardID, cardID, "safe-boundary-pause")
	close(reachBoundary)
	paused := waitPausePhase(t, ctx, store, boardID, cardID, workboard.PauseAcknowledged)
	if mutationErr := handle.AppendCheckpoint(ctx, "must be denied while paused"); !errors.Is(mutationErr, ErrAdmission) {
		t.Fatalf("paused capability remained usable: %v", mutationErr)
	}
	beforeRevision := paused.Attempt.Claim.Revision
	waitClaimRevision(t, ctx, store, boardID, cardID, beforeRevision)
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardResumeRequest,
		IdempotencyKey: "safe-boundary-resume", BoardID: boardID, CardID: cardID,
		ExpectedCardRevision: revisionPointer(card.Revision)}); err != nil {
		t.Fatal(err)
	}
	if err = waitError(t, boundaryDone, "safe boundary resume"); err != nil {
		t.Fatalf("safe boundary did not resume: %v", err)
	}
	if err = waitError(t, runDone, "resumed worker"); err != nil {
		t.Fatalf("resumed worker failed: %v", err)
	}
	card, err = store.GetCard(ctx, boardID, cardID)
	if err != nil || card.PausePhase != workboard.PauseNone || card.PauseRequested {
		t.Fatalf("resume acknowledgement not durable: card=%+v err=%v", card, err)
	}
}

func TestWorkboardWorkerSafeBoundaryCancellationPrecedesResume(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("9", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, reachBoundary := make(chan struct{}), make(chan struct{})
	boundaryDone, runDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "paused-cancel-task",
			SessionID: "paused-cancel-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, worker *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, worker); err != nil {
					return WorkboardCandidate{}, err
				}
				close(entered)
				<-reachBoundary
				boundaryErr := worker.SafeBoundary(run)
				boundaryDone <- boundaryErr
				return WorkboardCandidate{}, boundaryErr
			}, Validate: func(context.Context, WorkboardCandidate) error { return nil },
		})
		runDone <- runErr
	}()
	waitSignal(t, entered, "worker callback")
	requestWorkerPause(t, ctx, bridge, store, boardID, cardID, "paused-cancel-pause")
	close(reachBoundary)
	waitPausePhase(t, ctx, store, boardID, cardID, workboard.PauseAcknowledged)
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCancelRequest,
		IdempotencyKey: "paused-cancel-request", BoardID: boardID, CardID: cardID,
		ExpectedCardRevision: revisionPointer(card.Revision)}); err != nil {
		t.Fatal(err)
	}
	if err = waitError(t, boundaryDone, "paused cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not win while paused: %v", err)
	}
	if err = waitError(t, runDone, "canceled worker"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runner did not retain cancellation: %v", err)
	}
}

func requestWorkerPause(t *testing.T, ctx context.Context, bridge *WorkboardBridge, store interface {
	GetCard(context.Context, string, string) (workboard.Card, error)
}, boardID, cardID, key string) {
	t.Helper()
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardPauseRequest,
		IdempotencyKey: key, BoardID: boardID, CardID: cardID,
		ExpectedCardRevision: revisionPointer(card.Revision)}); err != nil {
		t.Fatal(err)
	}
}

func waitPausePhase(t *testing.T, ctx context.Context, store interface {
	GetCard(context.Context, string, string) (workboard.Card, error)
	ReadCardLifecycleSnapshots(context.Context, string, []string) (map[string]workboard.CardLifecycleSnapshot, error)
}, boardID, cardID string, phase workboard.PausePhase) workboard.CardLifecycleSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
		card, cardErr := store.GetCard(ctx, boardID, cardID)
		if err == nil && cardErr == nil && card.PausePhase == phase {
			return snapshots[cardID]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pause phase %q was not observed", phase)
	return workboard.CardLifecycleSnapshot{}
}

func waitClaimRevision(t *testing.T, ctx context.Context, store interface {
	ReadCardLifecycleSnapshots(context.Context, string, []string) (map[string]workboard.CardLifecycleSnapshot, error)
}, boardID, cardID string, before int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
		if err == nil && snapshots[cardID].Attempt != nil && snapshots[cardID].Attempt.Claim != nil &&
			snapshots[cardID].Attempt.Claim.Revision > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("claim heartbeat did not continue while paused")
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
