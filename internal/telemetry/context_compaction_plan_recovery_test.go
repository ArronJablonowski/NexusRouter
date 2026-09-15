package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestContextCompactionPlanDeadOwnerRecovery(t *testing.T) {
	if os.Getenv("DARWIN_COMPACTION_RECOVERY_CHILD") == "1" {
		ctx := context.Background()
		store, err := Open(ctx, os.Getenv("DARWIN_COMPACTION_RECOVERY_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		start, _ := compactionPlanStartFixture(t, store)
		if _, created, beginErr := store.BeginContextCompactionPlan(ctx, start); beginErr != nil || !created {
			t.Fatal(created, beginErr)
		}
		if os.Getenv("DARWIN_COMPACTION_RECOVERY_HOLD") == "1" {
			fmt.Println("ready")
			if _, err = io.Copy(io.Discard, os.Stdin); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "compaction-recovery.db")
	command := exec.Command(os.Args[0], "-test.run=^TestContextCompactionPlanDeadOwnerRecovery$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "DARWIN_COMPACTION_RECOVERY_CHILD=1", "DARWIN_COMPACTION_RECOVERY_DB="+path,
		"DARWIN_PROCESS_OWNER_DIR="+filepath.Join(directory, "dead-owner"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dead owner: %v: %s", err, output)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.ContextCompactionPlan(ctx, "compaction-operation")
	if err != nil || before.Status != sessions.ContextCompactionStarted || before.Start.ProcessID == "" {
		t.Fatalf("pre-recovery state: %+v %v", before, err)
	}
	// The generic summary sweeper may run in another process before the enclosing
	// compaction sweep. Its valid receipt must not strand the plan in started.
	if _, recovered, summaryErr := store.ReconcileSummaryAttemptsPage(ctx, "", 100, time.Unix(399, 0).UTC()); summaryErr != nil || recovered != 1 {
		t.Fatalf("generic summary recovery: recovered=%d err=%v", recovered, summaryErr)
	}
	now := time.Unix(400, 0).UTC()
	type result struct {
		recovered int
		err       error
	}
	const sweepers = 8
	results := make(chan result, sweepers)
	var wait sync.WaitGroup
	for range sweepers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, recovered, reconcileErr := store.ReconcileContextCompactionPlansPage(ctx, "", 100, now)
			results <- result{recovered, reconcileErr}
		}()
	}
	wait.Wait()
	close(results)
	total := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += result.recovered
	}
	if total != 1 {
		t.Fatalf("terminal fact count=%d", total)
	}
	after, err := store.ContextCompactionPlan(ctx, before.Start.OperationID)
	if err != nil || after.Status != sessions.ContextCompactionFailed || len(after.Facts) != 2 ||
		after.Facts[1].Code != "owner_interrupted" || after.Recovery == nil || after.Recovery.ProcessID == before.Start.ProcessID ||
		after.TerminalAttempt == nil || after.TerminalAttempt.Status != "interrupted" || after.TerminalAttempt.Code != "owner_interrupted" {
		t.Fatalf("recovered state: %+v %v", after, err)
	}
	receipt, err := store.ContextCompactionRecovery(ctx, before.Start.OperationID)
	if err != nil || receipt.Digest != after.Recovery.Digest || receipt.FailedFactDigest != after.Facts[1].Digest {
		t.Fatalf("recovery receipt: %+v %v", receipt, err)
	}
	if summaryReceipt, receiptErr := store.SummaryAttemptRecovery(ctx, before.Start.AttemptID); receiptErr != nil || summaryReceipt.Code != "owner_interrupted" {
		t.Fatalf("summary interruption missing: %+v %v", summaryReceipt, receiptErr)
	}
	stateBytes, _ := json.Marshal(after)
	receiptBytes, _ := json.Marshal(receipt)
	for range 3 {
		next, recovered, reconcileErr := store.ReconcileContextCompactionPlansPage(ctx, "", 100, now.Add(time.Hour))
		if reconcileErr != nil || next != "" || recovered != 0 {
			t.Fatalf("exact recovery replay: %q %d %v", next, recovered, reconcileErr)
		}
	}
	replayed, err := store.ContextCompactionPlan(ctx, before.Start.OperationID)
	replayedReceipt, receiptErr := store.ContextCompactionRecovery(ctx, before.Start.OperationID)
	replayedStateBytes, _ := json.Marshal(replayed)
	replayedReceiptBytes, _ := json.Marshal(replayedReceipt)
	if err != nil || receiptErr != nil || string(stateBytes) != string(replayedStateBytes) || string(receiptBytes) != string(replayedReceiptBytes) {
		t.Fatal("recovery replay changed immutable evidence", err, receiptErr)
	}
	if _, err = store.db.Exec(`DROP TRIGGER context_compaction_plan_recovery_immutable_update;
		UPDATE context_compaction_plan_recoveries SET failed_fact_digest=? WHERE operation_id=?`, strings.Repeat("f", 64), before.Start.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ContextCompactionPlan(ctx, before.Start.OperationID); !errors.Is(err, sessions.ErrContextCompactionLifecycle) {
		t.Fatal("normalized recovery corruption was trusted", err)
	}
}

func TestContextCompactionPlanLiveOwnerCannotRecover(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "live-compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start, _ := compactionPlanStartFixture(t, store)
	if _, created, err := store.BeginContextCompactionPlan(ctx, start); err != nil || !created {
		t.Fatal(created, err)
	}
	const sweepers = 8
	results := make(chan error, sweepers)
	for range sweepers {
		go func() {
			_, recovered, reconcileErr := store.ReconcileContextCompactionPlansPage(ctx, "", 100, time.Unix(400, 0).UTC())
			if reconcileErr == nil && recovered != 0 {
				reconcileErr = ErrContextCompactionRecovery
			}
			results <- reconcileErr
		}()
	}
	for range sweepers {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.ContextCompactionPlan(ctx, start.OperationID)
	if err != nil || state.Status != sessions.ContextCompactionStarted || len(state.Facts) != 1 {
		t.Fatalf("live owner changed: %+v %v", state, err)
	}
	if _, err = store.ContextCompactionRecovery(ctx, start.OperationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("live owner has recovery receipt: %v", err)
	}
}

func TestContextCompactionPlanDeadOwnerPreservesDurableDraft(t *testing.T) {
	if os.Getenv("DARWIN_COMPACTION_DRAFT_CHILD") == "1" {
		ctx := context.Background()
		store, err := Open(ctx, os.Getenv("DARWIN_COMPACTION_RECOVERY_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		start, draft := compactionPlanStartFixture(t, store)
		if _, created, beginErr := store.BeginContextCompactionPlan(ctx, start); beginErr != nil || !created {
			t.Fatal(created, beginErr)
		}
		attempt := summaryAttemptForCompactionStart(start)
		attempt.Status, attempt.Draft, attempt.FinishedAt = "drafted", draft, start.StartedAt.Add(time.Second)
		if err = store.CompleteSummary(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		return
	}

	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "durable-draft.db")
	command := exec.Command(os.Args[0], "-test.run=^TestContextCompactionPlanDeadOwnerPreservesDurableDraft$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "DARWIN_COMPACTION_DRAFT_CHILD=1", "DARWIN_COMPACTION_RECOVERY_DB="+path,
		"DARWIN_PROCESS_OWNER_DIR="+filepath.Join(directory, "draft-owner"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("draft owner: %v: %s", err, output)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.ContextCompactionPlan(ctx, "compaction-operation")
	if err != nil || before.Status != sessions.ContextCompactionStarted || before.TerminalAttempt == nil || before.TerminalAttempt.Status != "drafted" {
		t.Fatalf("durable draft unavailable before sweep: %+v %v", before, err)
	}
	if next, recovered, reconcileErr := store.ReconcileContextCompactionPlansPage(ctx, "", 100, time.Unix(400, 0).UTC()); reconcileErr != nil || next != "" || recovered != 0 {
		t.Fatalf("durable draft was treated as interrupted: %q %d %v", next, recovered, reconcileErr)
	}
	after, err := store.ContextCompactionPlan(ctx, before.Start.OperationID)
	if err != nil || after.StateDigest != before.StateDigest || after.TerminalAttempt == nil || after.TerminalAttempt.Status != "drafted" {
		t.Fatalf("durable draft changed after sweep: %+v %v", after, err)
	}
	if _, err = store.ContextCompactionRecovery(ctx, before.Start.OperationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("durable draft received recovery authority: %v", err)
	}
}
