package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const (
	recoveryFixtureTask    = "workboard-recovery-task"
	recoveryFixtureSession = "workboard-recovery-session"
	recoveryFixtureWorker  = "workboard-recovery-worker"
)

type recoveryFixture struct {
	store                       *Store
	path, boardID, cardID       string
	attemptID, claimID          string
	cardRevision, claimRevision int64
	clock                       time.Time
}

func newRecoveryFixture(t *testing.T, mode string) recoveryFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	card, boardID := readyLifecycleCard(t, ctx, store, now)
	start := runtime.Event{Version: 1, ID: "recovery-task-start", TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession,
		CorrelationID: recoveryFixtureTask, Sequence: 1, Time: now.Add(time.Second), Kind: runtime.TaskStarted}
	if err = store.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if mode != "clean" && mode != "running" && mode != "live" {
		appendRecoveryToolHistory(t, store, now, mode)
	}
	clock := card.UpdatedAt.Add(2 * time.Second)
	worker := newTestLifecycleService(t, store, workboard.Actor{ID: recoveryFixtureWorker, Type: "worker"},
		verifiedLifecycleRecovery("unused-recovery-proof", workboard.EffectFree), &clock)
	receipt, err := worker.Claim(ctx, workboard.ClaimRequest{BoardID: boardID, CardID: card.ID,
		IdempotencyKey: "production-recovery-claim", ExpectedCardRevision: card.Revision,
		TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, claimID := currentLifecycleIDs(t, store, boardID, card.ID)
	fixture := recoveryFixture{store: store, path: path, boardID: boardID, cardID: card.ID, attemptID: attemptID,
		claimID: claimID, cardRevision: *receipt.CardRevision, claimRevision: *receipt.ClaimRevision, clock: clock.Add(time.Minute)}
	if mode == "live" {
		lease, leaseErr := store.AcquireLease(ctx, recoveryFixtureTask, recoveryFixtureWorker, "workboard-fixture", false, time.Now().UTC(), time.Minute)
		if leaseErr != nil {
			t.Fatal(leaseErr)
		}
		snapshot, snapshotErr := store.TaskSnapshot(ctx, recoveryFixtureTask)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if err = store.FinishLeased(ctx, snapshot.Sequence, recoveryTerminal(snapshot.Sequence+1, now, runtime.TaskCompleted), lease.Token, lease.Owner); err != nil {
			t.Fatal(err)
		}
	} else {
		runRecoveryFixtureProcess(t, path, mode)
	}
	return fixture
}

func appendRecoveryToolHistory(t *testing.T, store *Store, now time.Time, mode string) {
	t.Helper()
	call := providers.ToolCall{ID: "recovery-call", Name: "fixture", Arguments: json.RawMessage(`{}`)}
	events := []runtime.Event{
		{Version: 1, ID: "recovery-turn-start", TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession, CorrelationID: recoveryFixtureTask,
			Sequence: 2, Time: now.Add(2 * time.Second), Kind: runtime.TurnStarted, TurnID: "recovery-turn", AttemptID: "recovery-model-attempt"},
		{Version: 1, ID: "recovery-turn-done", TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession, CorrelationID: recoveryFixtureTask,
			Sequence: 3, Time: now.Add(3 * time.Second), Kind: runtime.TurnCompleted, TurnID: "recovery-turn", AttemptID: "recovery-model-attempt",
			Data: runtime.Data{ToolCalls: []providers.ToolCall{call}}},
		{Version: 1, ID: "recovery-tool-start", TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession, CorrelationID: recoveryFixtureTask,
			Sequence: 4, Time: now.Add(4 * time.Second), Kind: runtime.ToolStarted, TurnID: "recovery-turn", AttemptID: "recovery-model-attempt",
			Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect}},
	}
	if mode != "pending" {
		effect := runtime.NoEffect
		if mode == "confirmed" {
			effect = runtime.ConfirmedEffect
		} else if mode == "uncertain" {
			effect = runtime.UncertainEffect
		}
		events = append(events, runtime.Event{Version: 1, ID: "recovery-tool-done", TaskID: recoveryFixtureTask,
			SessionID: recoveryFixtureSession, CorrelationID: recoveryFixtureTask, Sequence: 5, Time: now.Add(5 * time.Second),
			Kind: runtime.ToolCompleted, TurnID: "recovery-turn", AttemptID: "recovery-model-attempt",
			Data: runtime.Data{ToolCallID: call.ID, ToolName: call.Name, ToolBehavior: runtime.BehaviorReadOnly, Effect: effect}})
	}
	for _, event := range events {
		if err := store.Append(context.Background(), event.Sequence-1, event); err != nil {
			t.Fatal(mode, event.Kind, err)
		}
	}
}

func recoveryTerminal(sequence int64, now time.Time, kind runtime.Kind) runtime.Event {
	return runtime.Event{Version: 1, ID: "recovery-task-terminal", TaskID: recoveryFixtureTask, SessionID: recoveryFixtureSession,
		CorrelationID: recoveryFixtureTask, WorkerID: recoveryFixtureWorker, Sequence: sequence, Time: now.Add(time.Duration(sequence) * time.Second), Kind: kind}
}

func runRecoveryFixtureProcess(t *testing.T, path, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkboardRecoveryProofForeignHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"),
		"DARWIN_WORKBOARD_RECOVERY_HELPER=1", "DARWIN_WORKBOARD_RECOVERY_DB=" + path, "DARWIN_WORKBOARD_RECOVERY_MODE=" + mode}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("foreign recovery fixture: %v: %s", err, output)
	}
}

func TestWorkboardRecoveryProofForeignHelper(t *testing.T) {
	if os.Getenv("DARWIN_WORKBOARD_RECOVERY_HELPER") != "1" {
		return
	}
	ctx := context.Background()
	store, err := Open(ctx, os.Getenv("DARWIN_WORKBOARD_RECOVERY_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(ctx, recoveryFixtureTask, recoveryFixtureWorker, "workboard-fixture", false, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DARWIN_WORKBOARD_RECOVERY_MODE") == "running" {
		return
	}
	snapshot, err := store.TaskSnapshot(ctx, recoveryFixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.TaskCompleted
	if len(snapshot.Pending) != 0 || snapshot.UncertainEffects || len(snapshot.Messages) > 0 {
		kind = runtime.TaskFailed
	}
	if err = store.FinishLeased(ctx, snapshot.Sequence, recoveryTerminal(snapshot.Sequence+1, time.Now().UTC(), kind), lease.Token, lease.Owner); err != nil {
		t.Fatal(err)
	}
}

func TestWorkboardRecoveryVerifierRecoversEffectFreeStoppedTaskAndReplaysBeforeReverify(t *testing.T) {
	ctx := context.Background()
	fixture := newRecoveryFixture(t, "clean")
	actor := workboard.Actor{ID: "recovery-supervisor", Type: "system"}
	intent, err := fixture.store.PrepareWorkboardRecovery(ctx, WorkboardRecoveryTarget{fixture.boardID, fixture.cardID, fixture.attemptID, fixture.claimID}, actor)
	if err != nil || intent.EffectResolution != workboard.EffectFree {
		t.Fatal(intent, err)
	}
	executor, err := NewWorkboardRecoveryExecutor(fixture.store, actor, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	request := WorkboardClaimRecoveryRequest{BoardID: fixture.boardID, CardID: fixture.cardID, AttemptID: fixture.attemptID,
		ClaimID: fixture.claimID, IdempotencyKey: "production-recovery-apply", ExpectedCardRevision: fixture.cardRevision,
		ExpectedClaimRevision: fixture.claimRevision}
	if _, err = fixture.store.db.Exec(`CREATE TRIGGER fail_production_recovery BEFORE INSERT ON workboard_events WHEN NEW.kind='claim.recover' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = executor.RecoverClaim(ctx, request); err == nil {
		t.Fatal("recovery transaction did not roll back")
	}
	var proofCount int
	if err = fixture.store.db.QueryRow(`SELECT count(*) FROM workboard_recovery_proofs`).Scan(&proofCount); err != nil || proofCount != 0 {
		t.Fatal(proofCount, err)
	}
	if _, err = fixture.store.db.Exec(`DROP TRIGGER fail_production_recovery`); err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.RecoverClaim(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = Open(ctx, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.store.Close()
	// Exact replay must be served before the verifier touches now-corrupt
	// process metadata.
	if _, err = fixture.store.db.Exec(`UPDATE lease_processes SET body='{}'`); err != nil {
		t.Fatal(err)
	}
	executor, err = NewWorkboardRecoveryExecutor(fixture.store, actor, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := executor.RecoverClaim(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatal(replayed, receipt, err)
	}
}

func TestWorkboardRecoveryVerifierRejectsRunningLiveAndUnsafeEffects(t *testing.T) {
	actor := workboard.Actor{ID: "recovery-supervisor", Type: "system"}
	for _, mode := range []string{"running", "live", "pending", "confirmed", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRecoveryFixture(t, mode)
			defer fixture.store.Close()
			_, err := fixture.store.PrepareWorkboardRecovery(context.Background(), WorkboardRecoveryTarget{
				fixture.boardID, fixture.cardID, fixture.attemptID, fixture.claimID,
			}, actor)
			if !errors.Is(err, ErrWorkboardRecoveryProof) {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestWorkboardRecoveryVerifierAcceptsAttentionClaimOnlyWithIndependentProof(t *testing.T) {
	ctx := context.Background()
	fixture := newRecoveryFixture(t, "clean")
	defer fixture.store.Close()
	actor := workboard.Actor{ID: "recovery-supervisor", Type: "system"}
	observedAt := fixture.clock.Add(2 * time.Minute)
	attention, err := workboard.NewAttentionService(fixture.store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "trusted-supervisor", Actor: actor}}, func() time.Time { return observedAt }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := attention.Scan(ctx, fixture.boardID, 1)
	if err != nil || len(items) != 1 || items[0].ClaimID != fixture.claimID || items[0].ClaimRevision != 2 {
		t.Fatal(items, err)
	}
	executor, err := NewWorkboardRecoveryExecutor(fixture.store, actor, func() time.Time { return observedAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.RecoverClaim(ctx, WorkboardClaimRecoveryRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		AttemptID: fixture.attemptID, ClaimID: fixture.claimID, IdempotencyKey: "attention-production-recovery",
		ExpectedCardRevision: fixture.cardRevision, ExpectedClaimRevision: items[0].ClaimRevision})
	if err != nil || receipt.ClaimRevision == nil || *receipt.ClaimRevision != 3 {
		t.Fatal(receipt, err)
	}
}

func TestWorkboardRecoveryVerifierRejectsStaleOrCorruptEvidence(t *testing.T) {
	actor := workboard.Actor{ID: "recovery-supervisor", Type: "system"}
	for _, mode := range []string{"stale_head", "noncanonical_head", "corrupt_process", "missing_process"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRecoveryFixture(t, "clean")
			defer fixture.store.Close()
			var err error
			switch mode {
			case "stale_head":
				_, err = fixture.store.db.Exec(`UPDATE task_heads SET sequence=sequence-1 WHERE task_id=?`, recoveryFixtureTask)
			case "noncanonical_head":
				_, err = fixture.store.db.Exec(`UPDATE events SET body=body||' ' WHERE task_id=? AND sequence=(SELECT sequence FROM task_heads WHERE task_id=?)`, recoveryFixtureTask, recoveryFixtureTask)
			case "corrupt_process":
				_, err = fixture.store.db.Exec(`UPDATE lease_processes SET body='{}'`)
			case "missing_process":
				_, err = fixture.store.db.Exec(`UPDATE resource_leases SET process_id=NULL`)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.store.PrepareWorkboardRecovery(context.Background(), WorkboardRecoveryTarget{
				fixture.boardID, fixture.cardID, fixture.attemptID, fixture.claimID,
			}, actor)
			if !errors.Is(err, ErrWorkboardRecoveryProof) {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestWorkboardControlFinalizeUsesDurableRecoveryVerifier(t *testing.T) {
	ctx := context.Background()
	fixture := newRecoveryFixture(t, "clean")
	operator := workboard.Actor{ID: "recovery-operator", Type: "operator"}
	control, err := workboard.NewControlService(fixture.store, telemetryCardAuthority{authority: workboard.Authority{
		CreationScope: "trusted-operator", Actor: operator}}, fixture.store, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	cancelReceipt, err := control.RequestCancel(ctx, workboard.RequestCardControl{BoardID: fixture.boardID, CardID: fixture.cardID,
		IdempotencyKey: "production-cancel-request", ExpectedCardRevision: fixture.cardRevision})
	if err != nil || cancelReceipt.CardRevision == nil {
		t.Fatal(cancelReceipt, err)
	}
	executor, err := NewWorkboardRecoveryExecutor(fixture.store, operator, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executor.FinalizeCancel(ctx, WorkboardCancelFinalizationRequest{BoardID: fixture.boardID, CardID: fixture.cardID,
		AttemptID: fixture.attemptID, ClaimID: fixture.claimID, IdempotencyKey: "production-cancel-finalize",
		ExpectedCardRevision: *cancelReceipt.CardRevision, ExpectedClaimRevision: fixture.claimRevision})
	if err != nil || receipt.CardRevision == nil || receipt.ClaimRevision == nil {
		t.Fatal(receipt, err)
	}
	defer fixture.store.Close()
}
