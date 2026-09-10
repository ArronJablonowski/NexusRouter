package app

import (
	"context"
	"crypto/rand"
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

func bindTestWorkboardRuntime(ctx context.Context, handle *WorkboardWorkerHandle) error {
	request, err := handle.BindRuntimeRequest(Request{})
	if err != nil || request.runtimeHostAdmission == nil {
		return errors.Join(ErrAdmission, err)
	}
	admission := request.runtimeHostAdmission
	return admission.commit(ctx, runtime.Event{Version: 1, ID: rand.Text(), TaskID: admission.taskID,
		SessionID: admission.sessionID, CorrelationID: admission.taskID, WorkerID: admission.workerID,
		Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ParentTaskID: admission.parentTaskID}})
}

func TestWorkboardWorkerRunnerLeavesUnboundCardReady(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("1", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "unbound-runtime-task",
		SessionID: "unbound-runtime-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			called = true
			if mutationErr := handle.AppendCheckpoint(run, "not yet claimed"); !errors.Is(mutationErr, ErrAdmission) {
				return WorkboardCandidate{}, errors.New("pending handle admitted a mutation")
			}
			return WorkboardCandidate{Summary: "must not be submitted"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if !called || !errors.Is(err, ErrAdmission) {
		t.Fatalf("called=%v err=%v", called, err)
	}
	card, cardErr := store.GetCard(ctx, boardID, cardID)
	snapshots, snapshotErr := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	events, eventErr := store.Read(ctx, "unbound-runtime-task", 0, 10)
	if cardErr != nil || snapshotErr != nil || eventErr != nil || card.State != "ready" || card.CurrentAttemptID != "" ||
		snapshots[cardID].Attempt != nil || len(events) != 0 {
		t.Fatalf("unbound execution changed durable state: card=%+v lifecycle=%+v events=%+v errors=%v/%v/%v",
			card, snapshots[cardID], events, cardErr, snapshotErr, eventErr)
	}
}

func TestWorkboardWorkerRunnerAllowsTopLevelTaskAndFreezesAssignedIdentity(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	assigned := "worker-stable"
	revised, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardRevise,
		IdempotencyKey: "assign-stable-worker", BoardID: boardID, CardID: cardID, AssigneeID: assigned,
		ExpectedCardRevision: revisionPointer(cardRevision)})
	if err != nil || revised.CardRevision == nil {
		t.Fatalf("assign receipt=%+v err=%v", revised, err)
	}
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("4", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "assigned-top-task",
		SessionID: "assigned-top-session", WorkerID: assigned, Scope: "board-card-" + cardID,
		ExpectedCardRevision: *revised.CardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			if bindErr := bindTestWorkboardRuntime(run, handle); bindErr != nil {
				return WorkboardCandidate{}, bindErr
			}
			time.Sleep(45 * time.Millisecond)
			return WorkboardCandidate{Summary: "top-level candidate"}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if err != nil || candidate.Summary != "top-level candidate" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	events, err := store.Read(ctx, "assigned-top-task", 0, 100)
	if err != nil || len(events) != 1 || events[0].WorkerID != assigned || events[0].Data.ParentTaskID != "" {
		t.Fatalf("top-level identity journal=%+v err=%v", events, err)
	}
	states, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	attempt := states[cardID].Attempt
	if err != nil || attempt == nil || attempt.Claim == nil || attempt.WorkerID != assigned ||
		attempt.Claim.OwnerID != assigned || attempt.Claim.State != "released" || attempt.Claim.Revision < 2 {
		t.Fatalf("identity was not frozen through heartbeat: state=%+v err=%v", states[cardID], err)
	}
}

func TestWorkboardWorkerRunnerRejectsAssignedIdentityMismatchBeforeExecution(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardRevise,
		IdempotencyKey: "assign-mismatch-worker", BoardID: boardID, CardID: cardID, AssigneeID: "worker-right",
		ExpectedCardRevision: revisionPointer(cardRevision)})
	if err != nil || revised.CardRevision == nil {
		t.Fatalf("assign receipt=%+v err=%v", revised, err)
	}
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("5", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "assigned-mismatch-task",
		SessionID: "assigned-mismatch-session", WorkerID: "worker-wrong", Scope: "board-card-" + cardID,
		ExpectedCardRevision: *revised.CardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(context.Context, *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			called = true
			return WorkboardCandidate{}, nil
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if !errors.Is(err, ErrAdmission) || called {
		t.Fatalf("mismatched assigned worker admitted: called=%v err=%v", called, err)
	}
	card, cardErr := store.GetCard(ctx, boardID, cardID)
	if cardErr != nil || card.State != workboard.Ready || card.AssigneeID != "worker-right" || card.CurrentClaimID != "" {
		t.Fatalf("mismatch changed card: card=%+v err=%v", card, cardErr)
	}
}

func TestWorkboardWorkerHandleBindsRuntimeRequestOnlyOnce(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("2", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "single-bind-task",
		SessionID: "single-bind-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
		ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
		Execute: func(_ context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
			first, firstErr := handle.BindRuntimeRequest(Request{})
			if firstErr != nil || first.runtimeHostAdmission == nil {
				return WorkboardCandidate{}, errors.Join(errors.New("first bind failed"), firstErr)
			}
			if _, secondErr := handle.BindRuntimeRequest(Request{}); !errors.Is(secondErr, ErrAdmission) {
				return WorkboardCandidate{}, errors.New("second bind was admitted")
			}
			return WorkboardCandidate{}, errors.New("stop before runtime start")
		}, Validate: func(context.Context, WorkboardCandidate) error { return nil }})
	if err == nil {
		t.Fatal("bound-but-unstarted callback succeeded")
	}
	card, cardErr := store.GetCard(ctx, boardID, cardID)
	if cardErr != nil || card.State != "ready" || card.CurrentClaimID != "" {
		t.Fatalf("binding alone claimed card: card=%+v err=%v", card, cardErr)
	}
}

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
			if err := bindTestWorkboardRuntime(ctx, handle); err != nil {
				return WorkboardCandidate{}, err
			}
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
	if err != nil || len(events) != 1 || events[0].Kind != runtime.TaskStarted {
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
			if err := bindTestWorkboardRuntime(ctx, handle); err != nil {
				return WorkboardCandidate{}, err
			}
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
					Execute: func(ctx context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
						if err := bindTestWorkboardRuntime(ctx, handle); err != nil {
							return WorkboardCandidate{}, err
						}
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

func TestWorkboardWorkerRunnerConsumesDurableCancelAfterHeartbeatAndPreservesClaim(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	evaluator := &capturingWorkboardEvaluator{}
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, evaluator, strings.Repeat("e", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cleanupMutations := make(chan []error, 1)
	done := make(chan error, 1)
	const taskID = "durable-board-cancel-task"
	go func() {
		_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: taskID,
			SessionID: "durable-board-cancel-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, handle); err != nil {
					return WorkboardCandidate{}, err
				}
				close(entered)
				<-run.Done()
				// Cancellation cleanup must not retain an ambient workboard
				// capability merely by replacing the canceled context.
				cleanupMutations <- []error{
					handle.AppendCheckpoint(context.Background(), "late cancellation cleanup"),
					handle.Block(context.Background(), "late-cancel-block"),
					handle.Unblock(context.Background(), "late-cancel-unblock"),
				}
				close(canceled)
				// Deliberately model slow cancellation cleanup. Neither the
				// supervisor slot nor its resource reader may be released yet.
				<-release
				return WorkboardCandidate{}, run.Err()
			},
			Validate: func(context.Context, WorkboardCandidate) error { return nil },
		})
		done <- runErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker callback did not start")
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil || card.CurrentAttemptID == "" || card.CurrentClaimID == "" {
		t.Fatalf("claimed card=%+v err=%v", card, err)
	}
	requested, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCancelRequest,
		IdempotencyKey: "runner-cancel-request-01", BoardID: boardID, CardID: cardID,
		ExpectedCardRevision: revisionPointer(card.Revision)})
	if err != nil || requested.CardRevision == nil {
		t.Fatalf("cancel request=%+v err=%v", requested, err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("durable cancellation was not consumed after heartbeat")
	}
	for index, mutationErr := range <-cleanupMutations {
		if !errors.Is(mutationErr, ErrAdmission) {
			t.Fatalf("cleanup mutation %d retained board capability: %v", index, mutationErr)
		}
	}
	select {
	case err = <-done:
		t.Fatalf("runner released before callback joined: %v", err)
	default:
	}
	status, err := store.TaskLeaseStatus(ctx, taskID, time.Now().UTC())
	if err != nil || status.TaskState != "running" || status.Leases == nil || status.Leases.LiveReaders != 0 ||
		status.Leases.ReleasedReaders != 0 {
		t.Fatalf("workboard slot created a synthetic runtime resource lease: status=%+v err=%v", status, err)
	}
	before, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	lifecycle := before[cardID]
	if err != nil || lifecycle.Attempt == nil || lifecycle.Attempt.Claim == nil || lifecycle.CheckpointCount != 0 ||
		lifecycle.Attempt.State != "running" || lifecycle.Attempt.Claim.State != "active" ||
		lifecycle.Attempt.Claim.Revision < 2 {
		t.Fatalf("cancel was not observed after a durable heartbeat: lifecycle=%+v err=%v", lifecycle, err)
	}
	card, err = store.GetCard(ctx, boardID, cardID)
	if err != nil || !card.CancelRequested || card.CurrentAttemptID != lifecycle.Attempt.ID ||
		card.CurrentClaimID != lifecycle.Attempt.Claim.ID {
		t.Fatalf("workboard cancel binding changed: card=%+v lifecycle=%+v err=%v", card, lifecycle, err)
	}
	close(release)
	select {
	case err = <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("runner did not report cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish after callback joined")
	}
	events, err := store.Read(ctx, taskID, 0, 100)
	if err != nil || len(events) != 1 || events[0].Kind != runtime.TaskStarted {
		t.Fatalf("runtime cancellation events=%+v err=%v", events, err)
	}
	status, err = store.TaskLeaseStatus(ctx, taskID, time.Now().UTC())
	if err != nil || status.TaskState != "running" || status.Leases == nil || status.Leases.LiveReaders != 0 ||
		status.Leases.ExpiredReaders != 0 || status.Leases.ReleasedReaders != 0 {
		t.Fatalf("workboard runner synthesized runtime lease/finalization: status=%+v err=%v", status, err)
	}
	after, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	if err != nil || after[cardID].Attempt == nil || after[cardID].Attempt.Claim == nil ||
		after[cardID].Attempt.State != "running" || after[cardID].Attempt.Claim.State != "active" ||
		after[cardID].Attempt.Claim.Revision != lifecycle.Attempt.Claim.Revision {
		t.Fatalf("runner released or mutated workboard claim: before=%+v after=%+v err=%v", lifecycle, after[cardID], err)
	}
	card, err = store.GetCard(ctx, boardID, cardID)
	if err != nil || !card.CancelRequested || card.CurrentClaimID != lifecycle.Attempt.Claim.ID {
		t.Fatalf("runner cleared durable cancellation: card=%+v err=%v", card, err)
	}
	if evaluator.request.BoardID != "" {
		t.Fatalf("canceled output entered evaluation: %+v", evaluator.request)
	}
}

func TestWorkboardWorkerRunnerDoesNotConsumePauseAsCancellation(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, store, &capturingWorkboardEvaluator{}, strings.Repeat("f", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, callbackCanceled, release := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	done := make(chan error, 1)
	parent, cancelParent := context.WithCancel(ctx)
	defer cancelParent()
	go func() {
		_, runErr := runner.Run(parent, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "pause-not-cancel-task",
			SessionID: "pause-not-cancel-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, handle); err != nil {
					return WorkboardCandidate{}, err
				}
				close(entered)
				select {
				case <-run.Done():
					callbackCanceled <- struct{}{}
					return WorkboardCandidate{}, run.Err()
				case <-release:
					return WorkboardCandidate{}, errors.New("test release")
				}
			}, Validate: func(context.Context, WorkboardCandidate) error { return nil },
		})
		done <- runErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker callback did not start")
	}
	bridge, err := NewWorkboardBridge(store, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	card, err := store.GetCard(ctx, boardID, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardPauseRequest,
		IdempotencyKey: "runner-pause-request-01", BoardID: boardID, CardID: cardID,
		ExpectedCardRevision: revisionPointer(card.Revision)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	card, err = store.GetCard(ctx, boardID, cardID)
	if err != nil || card.PausePhase != workboard.PauseRequested {
		t.Fatalf("pause was acknowledged away from a safe boundary: card=%+v err=%v", card, err)
	}
	select {
	case <-callbackCanceled:
		t.Fatal("pause request was incorrectly consumed as cancellation")
	default:
	}
	close(release)
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("test failure unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not join")
	}
}

type failingWorkerControlRepository struct {
	*telemetry.Store
	failed chan struct{}
	once   atomic.Bool
}

func (r *failingWorkerControlRepository) ReadWorkerControl(context.Context,
	workboard.WorkerControlTarget,
) (workboard.WorkerControlObservation, error) {
	if r.once.CompareAndSwap(false, true) {
		close(r.failed)
	}
	return workboard.WorkerControlObservation{}, errors.New("injected worker control observation failure")
}

func TestWorkboardWorkerRunnerRevokesCapabilityBeforeObservationFailureCleanup(t *testing.T) {
	ctx := context.Background()
	store, boardID, cardID, cardRevision := readyWorkboardCard(t)
	defer store.Close()
	repository := &failingWorkerControlRepository{Store: store, failed: make(chan struct{})}
	supervisor, err := workers.New(1, 20*time.Millisecond, 500*time.Millisecond, store, store)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWorkboardWorkerRunner(supervisor, repository, &capturingWorkboardEvaluator{}, strings.Repeat("7", 64),
		20*time.Millisecond, 500*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	cleanupMutations := make(chan []error, 1)
	done := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, WorkboardWorkerTask{BoardID: boardID, CardID: cardID, TaskID: "observation-failure-task",
			SessionID: "observation-failure-session", ParentTaskID: "board-parent", Scope: "board-card-" + cardID,
			ExpectedCardRevision: cardRevision, FailureEffect: runtime.NoEffect,
			Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
				if err := bindTestWorkboardRuntime(run, handle); err != nil {
					return WorkboardCandidate{}, err
				}
				close(entered)
				<-run.Done()
				cleanupMutations <- []error{
					handle.AppendCheckpoint(context.Background(), "escaped observation cleanup"),
					handle.Block(context.Background(), "escaped-observation-block"),
					handle.Unblock(context.Background(), "escaped-observation-unblock"),
				}
				<-release
				return WorkboardCandidate{}, run.Err()
			},
			Validate: func(context.Context, WorkboardCandidate) error { return nil },
		})
		done <- runErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker callback did not start")
	}
	select {
	case <-repository.failed:
	case <-time.After(3 * time.Second):
		t.Fatal("worker control observation did not fail")
	}
	var mutationErrors []error
	select {
	case mutationErrors = <-cleanupMutations:
	case <-time.After(3 * time.Second):
		t.Fatal("callback cleanup was not awakened")
	}
	for index, mutationErr := range mutationErrors {
		if !errors.Is(mutationErr, ErrAdmission) {
			t.Fatalf("cleanup mutation %d retained capability after observation failure: %v", index, mutationErr)
		}
	}
	select {
	case err = <-done:
		t.Fatalf("runner released before callback cleanup joined: %v", err)
	default:
	}
	before, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	lifecycle := before[cardID]
	if err != nil || lifecycle.Attempt == nil || lifecycle.Attempt.Claim == nil || lifecycle.CheckpointCount != 0 ||
		lifecycle.Attempt.State != "running" || lifecycle.Attempt.Claim.State != "active" || lifecycle.Attempt.Claim.Revision < 2 {
		t.Fatalf("observation failure changed durable claim: lifecycle=%+v err=%v", lifecycle, err)
	}
	claimRevision := lifecycle.Attempt.Claim.Revision
	close(release)
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("observation failure unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish after callback joined")
	}
	after, err := store.ReadCardLifecycleSnapshots(ctx, boardID, []string{cardID})
	if err != nil || after[cardID].Attempt == nil || after[cardID].Attempt.Claim == nil || after[cardID].CheckpointCount != 0 ||
		after[cardID].Attempt.State != "running" || after[cardID].Attempt.Claim.State != "active" ||
		after[cardID].Attempt.Claim.Revision != claimRevision {
		t.Fatalf("runner mutated claim after observation failure: before=%+v after=%+v err=%v", lifecycle, after[cardID], err)
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
				Execute: func(run context.Context, handle *WorkboardWorkerHandle) (WorkboardCandidate, error) {
					if err := bindTestWorkboardRuntime(run, handle); err != nil {
						return WorkboardCandidate{}, err
					}
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
