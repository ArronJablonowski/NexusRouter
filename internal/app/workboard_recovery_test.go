package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const (
	coordinatorTask    = "coordinator-recovery-task"
	coordinatorSession = "coordinator-recovery-session"
	coordinatorWorker  = "coordinator-recovery-worker"
)

type coordinatorEvaluator struct{}

func (coordinatorEvaluator) EvaluateCandidate(context.Context, workboard.CandidateEvaluationRequest) ([]workboard.EvidenceInput, error) {
	return nil, ErrAdmission
}

func TestWorkboardRecoveryCoordinatorRecoversOnlyDurablyStoppedAttentionClaim(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workboard.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Now().UTC().Add(-time.Minute)
	bridge, err := NewWorkboardBridge(store, store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	boardTitle := "Recovery"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "coordinator-board-key-01", Title: &boardTitle})
	if err != nil {
		t.Fatal(err)
	}
	cardTitle := "Durable work"
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass", Required: true}}
	card, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
		IdempotencyKey: "coordinator-card-key-001", BoardID: board.BoardID, Title: &cardTitle, Criteria: criteria,
		ExpectedBoardRevision: revisionPointer(board.BoardRevision), ExpectedGraphRevision: revisionPointer(1)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardMove,
		IdempotencyKey: "coordinator-ready-key-01", BoardID: board.BoardID, CardID: card.CardID, TargetState: "ready",
		ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision), ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision),
		ExpectedCardRevision: card.CardRevision})
	if err != nil {
		t.Fatal(err)
	}
	clock = time.Now().UTC()
	if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: "coordinator-task-start", TaskID: coordinatorTask,
		SessionID: coordinatorSession, CorrelationID: coordinatorTask, Sequence: 1, Time: clock, Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	dispatch, err := NewWorkboardWorkerDispatch(store, coordinatorWorker, strings.Repeat("a", 64), time.Second,
		coordinatorEvaluator{}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dispatch.Claim(ctx, workboard.ClaimRequest{BoardID: board.BoardID, CardID: card.CardID,
		IdempotencyKey: "coordinator-claim-key-01", ExpectedCardRevision: *ready.CardRevision,
		TaskID: coordinatorTask, SessionID: coordinatorSession}); err != nil {
		t.Fatal(err)
	}
	runCoordinatorRecoveryProcess(t, path)
	recoveryClock := time.Now().UTC().Add(workboard.MaxLeaseTTL + time.Minute)
	coordinator, err := NewWorkboardRecoveryCoordinator(store, func() time.Time { return recoveryClock })
	if err != nil {
		t.Fatal(err)
	}
	next, recovered, err := coordinator.RecoverAttentionPage(ctx, "")
	if err != nil || next != "" || recovered != 1 {
		t.Fatal(next, recovered, err)
	}
	after, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil || len(after.Cards) != 1 || after.Cards[0].State != "ready" || after.Cards[0].CurrentClaimID != "" ||
		len(after.Lifecycle) != 1 || after.Lifecycle[0].Attempt.State != "failed" {
		t.Fatalf("recovery projection=%+v err=%v", after, err)
	}
	// The bounded reconciliation call releases ownership only. It does not
	// claim, assign, or execute a replacement worker.
	again, recovered, err := coordinator.RecoverAttentionPage(ctx, "")
	if err != nil || again != "" || recovered != 0 {
		t.Fatal(again, recovered, err)
	}
}

func TestWorkboardRecoveryCoordinatorRotatesPastUnrecoverableAttentionPage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workboard.db")
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Now().UTC()
	bridge, err := NewWorkboardBridge(store, store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	title := "Recovery rotation"
	board, err := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.BoardCreate,
		IdempotencyKey: "coordinator-rotation-board", Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := NewWorkboardWorkerDispatch(store, coordinatorWorker, strings.Repeat("a", 64), time.Second,
		coordinatorEvaluator{}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	criteria := []webui.AcceptanceCriterion{{Version: 1, ID: "tests", Kind: "objective", RequiredSource: "deterministic",
		ValidatorID: "go-test", Description: "Tests pass", Required: true}}
	var recoverableCard string
	createdCards := make([]webui.OperationReceipt, 0, workboardRecoveryClaimLimit+1)
	for i := 0; i <= workboardRecoveryClaimLimit; i++ {
		snapshot, readErr := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
		if readErr != nil {
			t.Fatal(readErr)
		}
		cardTitle := fmt.Sprintf("Claim %02d", i)
		created, createErr := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardCreate,
			IdempotencyKey: fmt.Sprintf("coordinator-rotation-card-%02d", i), BoardID: board.BoardID, Title: &cardTitle,
			Criteria: criteria, ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision),
			ExpectedGraphRevision: revisionPointer(snapshot.GraphRevision)})
		if createErr != nil {
			t.Fatal(i, createErr)
		}
		createdCards = append(createdCards, created)
	}
	for i, created := range createdCards {
		snapshot, readErr := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
		if readErr != nil {
			t.Fatal(readErr)
		}
		ready, moveErr := bridge.NativeMutate(ctx, webui.BoardRequest{Version: 1, Action: webui.CardMove,
			IdempotencyKey: fmt.Sprintf("coordinator-rotation-ready-%02d", i), BoardID: board.BoardID,
			CardID: created.CardID, TargetState: "ready", ExpectedBoardRevision: revisionPointer(snapshot.Board.Revision),
			ExpectedLayoutRevision: revisionPointer(snapshot.Board.LayoutRevision), ExpectedCardRevision: created.CardRevision})
		if moveErr != nil {
			t.Fatal(i, moveErr)
		}
		clock = clock.Add(2 * time.Second)
		taskID, sessionID := fmt.Sprintf("rotation-task-%02d", i), fmt.Sprintf("rotation-session-%02d", i)
		if i == workboardRecoveryClaimLimit {
			taskID, sessionID, recoverableCard = coordinatorTask, coordinatorSession, created.CardID
		}
		if err = store.Append(ctx, 0, runtime.Event{Version: 1, ID: fmt.Sprintf("rotation-start-%02d", i), TaskID: taskID,
			SessionID: sessionID, CorrelationID: taskID, Sequence: 1, Time: clock, Kind: runtime.TaskStarted}); err != nil {
			t.Fatal(i, err)
		}
		if _, err = dispatch.Claim(ctx, workboard.ClaimRequest{BoardID: board.BoardID, CardID: created.CardID,
			IdempotencyKey: fmt.Sprintf("coordinator-rotation-claim-%02d", i), ExpectedCardRevision: *ready.CardRevision,
			TaskID: taskID, SessionID: sessionID}); err != nil {
			t.Fatal(i, err)
		}
	}
	runCoordinatorRecoveryProcess(t, path)
	recoveryClock := clock.Add(workboard.MaxLeaseTTL + time.Minute)
	coordinator, err := NewWorkboardRecoveryCoordinator(store, func() time.Time { return recoveryClock })
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 4 && total == 0; i++ {
		recoveryClock = recoveryClock.Add(time.Second)
		_, recovered, recoverErr := coordinator.RecoverAttentionPage(ctx, "")
		if recoverErr != nil {
			t.Fatal(i, recoverErr)
		}
		total += recovered
	}
	if total != 1 {
		t.Fatalf("later provable claim remained starved: recovered=%d", total)
	}
	after, err := bridge.NativeRead(ctx, board.BoardID, webui.BoardSnapshotOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, card := range after.Cards {
		if card.ID == recoverableCard {
			found = card.State == "ready" && card.CurrentClaimID == ""
		}
	}
	if !found {
		t.Fatal("recoverable card was not released after cursor rotation")
	}
}

func runCoordinatorRecoveryProcess(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkboardRecoveryCoordinatorForeignHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"),
		"DARWIN_WORKBOARD_COORDINATOR_HELPER=1", "DARWIN_WORKBOARD_COORDINATOR_DB=" + path}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery helper: %v: %s", err, output)
	}
}

func TestWorkboardRecoveryCoordinatorForeignHelper(t *testing.T) {
	if os.Getenv("DARWIN_WORKBOARD_COORDINATOR_HELPER") != "1" {
		return
	}
	ctx := context.Background()
	store, err := telemetry.Open(ctx, os.Getenv("DARWIN_WORKBOARD_COORDINATOR_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(ctx, coordinatorTask, coordinatorWorker, "workboard-coordinator", false, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	terminal := runtime.Event{Version: 1, ID: "coordinator-task-done", TaskID: coordinatorTask, SessionID: coordinatorSession,
		CorrelationID: coordinatorTask, WorkerID: coordinatorWorker, Sequence: 2, Time: time.Now().UTC(), Kind: runtime.TaskCompleted}
	if err = store.FinishLeased(ctx, 1, terminal, lease.Token, lease.Owner); err != nil {
		t.Fatal(err)
	}
}
