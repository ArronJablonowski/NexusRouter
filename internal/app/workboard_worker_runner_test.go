package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestWorkboardWorkerRunnerBindsRuntimeAndCompletesReviewLifecycle(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	evaluator := &capturingWorkboardEvaluator{}
	supervisor, err := workers.New(1, 50*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("a", 64),
		50*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkboardCandidate{Summary: "bounded implementation completed", ArtifactRefs: []string{"runtime-task-artifact"}}
	var validations atomic.Int32
	var retained *WorkboardWorkerHandle
	got, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "board-runtime-task",
		SessionID: "board-runtime-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(ctx context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			retained = handle
			if err := handle.AppendCheckpoint(ctx, "implementation compiled"); err != nil {
				return WorkboardCandidate{}, err
			}
			if err := handle.Block(ctx, "provider-pressure"); err != nil {
				return WorkboardCandidate{}, err
			}
			if err := handle.Unblock(ctx, "provider-pressure"); err != nil {
				return WorkboardCandidate{}, err
			}
			time.Sleep(120 * time.Millisecond)
			return want, nil
		},
		Validate: func(_ context.Context, candidate WorkboardCandidate) error {
			validations.Add(1)
			if candidate.Summary != want.Summary {
				return errors.New("candidate mismatch")
			}
			return nil
		},
	})
	if err != nil || got.Summary != want.Summary || len(got.ArtifactRefs) != 1 || validations.Load() != 1 {
		t.Fatalf("candidate=%+v validations=%d err=%v", got, validations.Load(), err)
	}
	if err = retained.AppendCheckpoint(ctx, "late escaped capability"); !errors.Is(err, ErrAdmission) {
		t.Fatalf("worker capability remained live after Run: %v", err)
	}
	snapshots, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	lifecycle := snapshots[cardID]
	if err != nil || lifecycle.Attempt == nil || lifecycle.Attempt.Claim == nil || lifecycle.Attempt.Candidate == nil {
		t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
	}
	attempt, claim := lifecycle.Attempt, lifecycle.Attempt.Claim
	if claim.TaskID != "board-runtime-task" || attempt.WorkerID != claim.OwnerID || claim.State != "released" ||
		!containsExact(attempt.TaskIDs, "board-runtime-task") || !containsExact(attempt.SessionIDs, "board-runtime-session") ||
		attempt.Candidate.Summary != want.Summary || lifecycle.CheckpointCount != 1 || len(lifecycle.Checkpoints) != 1 {
		t.Fatalf("incorrect durable binding: %+v", lifecycle)
	}
	events, err := store.Read(ctx, "board-runtime-task", 0, 100)
	if err != nil || len(events) < 5 || events[len(events)-1].Kind != runtime.TaskCompleted {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for _, event := range events {
		if event.WorkerID != claim.OwnerID {
			t.Fatalf("runtime/workboard worker identity diverged: event=%+v claim=%+v", event, claim)
		}
	}
}

func TestWorkboardWorkerRunnerDoesNotRetryAmbiguousExecution(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	evaluator := &capturingWorkboardEvaluator{}
	supervisor, err := workers.New(1, 50*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("b", 64),
		50*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	_, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "ambiguous-runtime-task",
		SessionID: "ambiguous-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, FailureEffect: runtime.UncertainEffect,
		Execute: func(ctx context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			calls.Add(1)
			if checkpointErr := handle.AppendCheckpoint(ctx, "external effect may have started"); checkpointErr != nil {
				return WorkboardCandidate{}, checkpointErr
			}
			return WorkboardCandidate{}, errors.New("effect acknowledgement unavailable")
		},
		Validate: func(context.Context, WorkboardCandidate) error { return nil },
	})
	if err == nil || calls.Load() != 1 || evaluator.request.BoardID != "" {
		t.Fatalf("err=%v calls=%d evaluator=%+v", err, calls.Load(), evaluator.request)
	}
	snapshots, readErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	lifecycle := snapshots[cardID]
	if readErr != nil || lifecycle.Attempt == nil || lifecycle.Attempt.Claim == nil || lifecycle.Attempt.Candidate != nil ||
		lifecycle.Attempt.Claim.TaskID != "ambiguous-runtime-task" || lifecycle.Attempt.Claim.State != "active" ||
		lifecycle.Attempt.State != "running" {
		t.Fatalf("ambiguous execution was hidden or retried: lifecycle=%+v err=%v", lifecycle, readErr)
	}
	snapshot, snapshotErr := store.ReadWorkboard(ctx, boardID, workboardSnapshotOptions())
	if snapshotErr != nil || len(snapshot.Cards) != 1 || snapshot.Cards[0].State != "blocked" || snapshot.Board.ActiveClaims != 1 {
		t.Fatalf("ambiguous effect was not held for manual review: snapshot=%+v err=%v", snapshot, snapshotErr)
	}
}

func TestWorkboardWorkerRunnerJoinsHeartbeatOnPanicAndCancellation(t *testing.T) {
	for _, mode := range []string{"panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, boardID, cardID, cardRevision := readyWorkboardCard(t)
			defer store.Close()
			evaluator := &capturingWorkboardEvaluator{}
			supervisor, err := workers.New(1, 50*time.Millisecond, 500*time.Millisecond, store, store)
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("c", 64),
				50*time.Millisecond, 500*time.Millisecond, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			entered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "joined-" + mode + "-task",
					SessionID: "joined-" + mode + "-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
					ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
					Execute: func(ctx context.Context, _ *WorkboardWorkerHandle) (WorkboardCandidate, error) {
						close(entered)
						if mode == "panic" {
							time.Sleep(80 * time.Millisecond)
							panic("worker callback panic")
						}
						<-ctx.Done()
						return WorkboardCandidate{}, ctx.Err()
					},
					Validate: func(context.Context, WorkboardCandidate) error { return nil },
				})
				done <- runErr
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not enter callback")
			}
			if mode == "cancel" {
				time.Sleep(80 * time.Millisecond)
				cancel()
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("failed worker returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not join")
			}
			cancel()
			snapshots, err := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
			if err != nil || snapshots[cardID].Attempt == nil || snapshots[cardID].Attempt.Claim == nil {
				t.Fatalf("snapshot=%+v err=%v", snapshots, err)
			}
			claimRevision := snapshots[cardID].Attempt.Claim.Revision
			if snapshots[cardID].Attempt.State != "failed" || snapshots[cardID].Attempt.Claim.State != "released" {
				t.Fatalf("effect-free failure remained active: %+v", snapshots[cardID])
			}
			events, err := store.Read(context.Background(), "joined-"+mode+"-task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(120 * time.Millisecond)
			after, err := store.ReadCardLifecycleSnapshots(context.Background(), boardID, []string{cardID})
			afterEvents, eventErr := store.Read(context.Background(), "joined-"+mode+"-task", 0, 100)
			if err != nil || eventErr != nil || after[cardID].Attempt.Claim.Revision != claimRevision || len(afterEvents) != len(events) {
				t.Fatalf("heartbeat outlived run: before=%d/%d after=%d/%d err=%v/%v",
					claimRevision, len(events), after[cardID].Attempt.Claim.Revision, len(afterEvents), err, eventErr)
			}
		})
	}
}

func TestWorkboardWorkerRunnerFinalizesEffectFreeExecuteAndValidationFailures(t *testing.T) {
	for _, mode := range []string{"execute", "validate"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, boardID, cardID, cardRevision := readyWorkboardCard(t)
			defer store.Close()
			evaluator := &capturingWorkboardEvaluator{}
			supervisor, err := workers.New(1, 50*time.Millisecond, 500*time.Millisecond, store, store)
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("d", 64),
				50*time.Millisecond, 500*time.Millisecond, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			var validations atomic.Int32
			_, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "effect-free-" + mode,
				SessionID: "effect-free-" + mode + "-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
				ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
				Execute: func(context.Context, *WorkboardWorkerHandle) (WorkboardCandidate, error) {
					if mode == "execute" {
						return WorkboardCandidate{}, errors.New("execution failed before effects")
					}
					return WorkboardCandidate{Summary: "candidate"}, nil
				},
				Validate: func(context.Context, WorkboardCandidate) error {
					validations.Add(1)
					return errors.New("deterministic validation failed")
				},
			})
			if err == nil || (mode == "execute" && validations.Load() != 0) || (mode == "validate" && validations.Load() != 1) {
				t.Fatalf("err=%v validations=%d", err, validations.Load())
			}
			lifecycle, readErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
			attempt := lifecycle[cardID].Attempt
			if readErr != nil || attempt == nil || attempt.Claim == nil || attempt.State != "failed" || attempt.Claim.State != "released" || attempt.Candidate != nil {
				t.Fatalf("lifecycle=%+v err=%v", lifecycle, readErr)
			}
			snapshot, snapshotErr := store.ReadWorkboard(ctx, boardID, workboardSnapshotOptions())
			if snapshotErr != nil || len(snapshot.Cards) != 1 || snapshot.Cards[0].State != "ready" || snapshot.Cards[0].AssigneeID != "" || snapshot.Board.ActiveClaims != 0 {
				t.Fatalf("snapshot=%+v err=%v", snapshot, snapshotErr)
			}
		})
	}
}

func workboardSnapshotOptions() workboard.BoardSnapshotOptions {
	return workboard.BoardSnapshotOptions{Limit: 100}
}

func readyWorkboardCard(t *testing.T) (*telemetry.Store, string, string, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "workboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	title := "Runtime binding"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "runner-board-create-01", Title: &title})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	cardTitle := "Execute bounded work"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Focused tests pass", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "runner-card-create-001", BoardID: board.BoardID, Title: &cardTitle, Criteria: criteria,
		ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	ready, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardMove,
		IdempotencyKey: "runner-card-ready-001", BoardID: board.BoardID, CardID: card.CardID, TargetState: "ready",
		ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision), ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision),
		ExpectedCardRevision: card.CardRevision})
	if err != nil || ready.CardRevision == nil {
		store.Close()
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	return store, board.BoardID, card.CardID, *ready.CardRevision
}
