package telemetry

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/accounting"
)

func TestSummaryRecoveryOwnerProcess(t *testing.T) {
	if os.Getenv("DARWIN_SUMMARY_RECOVERY_CHILD") == "1" {
		ctx := context.Background()
		store, err := Open(ctx, os.Getenv("DARWIN_SUMMARY_RECOVERY_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		attempt, _ := summaryAttemptFixture(t, store)
		ids := []string{attempt.ID}
		if configured := os.Getenv("DARWIN_SUMMARY_RECOVERY_IDS"); configured != "" {
			ids = strings.Split(configured, ",")
		}
		for index, id := range ids {
			owned := attempt
			owned.ID = id
			owned.StartedAt = attempt.StartedAt.Add(time.Duration(index) * time.Second)
			if err = store.BeginSummary(ctx, owned); err != nil {
				t.Fatal(err)
			}
		}
		if os.Getenv("DARWIN_SUMMARY_RECOVERY_HOLD") == "1" {
			fmt.Println("ready")
			if _, err = io.Copy(io.Discard, os.Stdin); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "summary-recovery.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := summaryAttemptFixture(t, store)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestSummaryRecoveryOwnerProcess$", "-test.timeout=20s")
	command.Env = append(os.Environ(),
		"DARWIN_SUMMARY_RECOVERY_CHILD=1",
		"DARWIN_SUMMARY_RECOVERY_DB="+path,
		"DARWIN_PROCESS_OWNER_DIR="+filepath.Join(dir, "owners"))
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("owner process: %v: %s", runErr, output)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(300, 0).UTC()
	type sweepResult struct {
		next      string
		recovered int
		err       error
	}
	const sweepers = 8
	results := make(chan sweepResult, sweepers)
	for range sweepers {
		go func() {
			next, recovered, reconcileErr := store.ReconcileSummaryAttemptsPage(ctx, "", 1, now)
			results <- sweepResult{next, recovered, reconcileErr}
		}()
	}
	total := 0
	for range sweepers {
		result := <-results
		if result.err != nil {
			t.Fatal("concurrent reconcile", result.err)
		}
		total += result.recovered
	}
	if total != 1 {
		t.Fatal("terminal outcome count", total)
	}
	terminal, err := store.SummaryAttempt(ctx, attempt.ID)
	if err != nil || terminal.Status != "interrupted" || terminal.Code != "owner_interrupted" || terminal.Draft != nil || terminal.Usage != nil || terminal.Elapsed != 0 || !terminal.FinishedAt.Equal(now) {
		t.Fatalf("terminal: %#v %v", terminal, err)
	}
	receipt, err := store.SummaryAttemptRecovery(ctx, attempt.ID)
	if err != nil || receipt.Validate() != nil || receipt.AttemptID != attempt.ID || receipt.TaskID != attempt.TaskID || receipt.SourceDigest != attempt.SourceDigest || receipt.SourceSequence != attempt.SourceSequence || receipt.State != terminal.Status || receipt.Code != terminal.Code || !receipt.RecoveredAt.Equal(now) {
		t.Fatalf("receipt: %#v %v", receipt, err)
	}
	page, err := store.ListSummaryAttemptRecoveries(ctx, attempt.TaskID, "", 1)
	if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], receipt) {
		t.Fatal("inspection page", page, err)
	}
	usage, err := store.CurrentUsage(ctx, usageID(accounting.Summarizer, attempt.ID))
	if err != nil || usage.Disposition != accounting.Failed || usage.RetryClass != accounting.Uncertain || usage.Usage != nil || usage.EvidenceID != attempt.ID {
		t.Fatalf("uncertain accounting: %#v %v", usage, err)
	}
	var derived int
	if err = store.db.QueryRow(`SELECT
		(SELECT count(*) FROM summary_reviews WHERE attempt_id=?)+
		(SELECT count(*) FROM summary_review_heads WHERE attempt_id=?)+
		(SELECT count(*) FROM events WHERE json_extract(body,'$.data.compaction.summary_attempt_id')=?)`, attempt.ID, attempt.ID, attempt.ID).Scan(&derived); err != nil || derived != 0 {
		t.Fatal("uncertain output derived authority", derived, err)
	}

	beforeAttempt, _ := json.Marshal(terminal)
	beforeReceipt, _ := json.Marshal(receipt)
	for range 3 {
		next, recovered, err := store.ReconcileSummaryAttemptsPage(ctx, "", 100, now.Add(time.Hour))
		if err != nil || next != "" || recovered != 0 {
			t.Fatal("idempotent replay", next, recovered, err)
		}
	}
	againAttempt, err := store.SummaryAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	againReceipt, err := store.SummaryAttemptRecovery(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterAttempt, _ := json.Marshal(againAttempt)
	afterReceipt, _ := json.Marshal(againReceipt)
	if string(beforeAttempt) != string(afterAttempt) || string(beforeReceipt) != string(afterReceipt) {
		t.Fatal("reconciliation changed durable bytes")
	}
	if _, err = store.ListSummaryAttemptRecoveries(ctx, "", "", 101); err == nil {
		t.Fatal("unbounded recovery inspection")
	}
	if _, err = store.SummaryAttemptRecovery(ctx, " missing"); err == nil {
		t.Fatal("invalid inspection identity")
	}
}

func TestSummaryRecoveryMultiPageRotationRevisitsOwner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "summary-rotation.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := summaryAttemptFixture(t, store)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	ownerCommand := exec.Command(os.Args[0], "-test.run=^TestSummaryRecoveryOwnerProcess$", "-test.timeout=30s")
	ownerCommand.Env = append(os.Environ(),
		"DARWIN_SUMMARY_RECOVERY_CHILD=1",
		"DARWIN_SUMMARY_RECOVERY_HOLD=1",
		"DARWIN_SUMMARY_RECOVERY_IDS=rotation-live",
		"DARWIN_SUMMARY_RECOVERY_DB="+path,
		"DARWIN_PROCESS_OWNER_DIR="+filepath.Join(dir, "live-owners"))
	ownerInput, err := ownerCommand.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	ownerOutput, err := ownerCommand.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = ownerCommand.Start(); err != nil {
		t.Fatal(err)
	}
	ownerDone := false
	t.Cleanup(func() {
		_ = ownerInput.Close()
		if !ownerDone {
			_ = ownerCommand.Process.Kill()
			_ = ownerCommand.Wait()
		}
	})
	if line, readErr := bufio.NewReader(ownerOutput).ReadString('\n'); readErr != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("live owner readiness: %q %v", line, readErr)
	}

	deadIDs := []string{"rotation-dead-1", "rotation-dead-2", "rotation-dead-3"}
	deadCommand := exec.Command(os.Args[0], "-test.run=^TestSummaryRecoveryOwnerProcess$", "-test.timeout=30s")
	deadCommand.Env = append(os.Environ(),
		"DARWIN_SUMMARY_RECOVERY_CHILD=1",
		"DARWIN_SUMMARY_RECOVERY_IDS="+strings.Join(deadIDs, ","),
		"DARWIN_SUMMARY_RECOVERY_DB="+path,
		"DARWIN_PROCESS_OWNER_DIR="+filepath.Join(dir, "dead-owners"))
	if output, runErr := deadCommand.CombinedOutput(); runErr != nil {
		t.Fatalf("dead owners: %v: %s", runErr, output)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0).UTC()
	cursor, recovered, err := store.ReconcileSummaryAttemptsPage(ctx, "", 2, now)
	if err != nil || cursor == "" || recovered != 1 {
		t.Fatal("first page", cursor, recovered, err)
	}
	firstCursor := cursor
	cursor, recovered, err = store.ReconcileSummaryAttemptsPage(ctx, cursor, 2, now)
	if err != nil || cursor == "" || cursor == firstCursor || recovered != 2 {
		t.Fatal("tail page", cursor, recovered, err)
	}
	cursor, recovered, err = store.ReconcileSummaryAttemptsPage(ctx, cursor, 2, now)
	if err != nil || cursor != "" || recovered != 0 {
		t.Fatal("cursor reset", cursor, recovered, err)
	}
	live, err := store.SummaryAttempt(ctx, "rotation-live")
	if err != nil || live.Status != "started" {
		t.Fatal("held owner was not skipped", live, err)
	}
	for _, id := range deadIDs {
		attempt, readErr := store.SummaryAttempt(ctx, id)
		if readErr != nil || attempt.Status != "interrupted" {
			t.Fatal("dead attempt not settled", id, attempt, readErr)
		}
	}

	if err = ownerInput.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ownerCommand.Wait(); err != nil {
		t.Fatal(err)
	}
	ownerDone = true
	cursor, recovered, err = store.ReconcileSummaryAttemptsPage(ctx, "", 2, now.Add(time.Second))
	if err != nil || cursor != "" || recovered != 1 {
		t.Fatal("revisit stopped owner", cursor, recovered, err)
	}

	allIDs := append([]string{"rotation-live"}, deadIDs...)
	before := make(map[string]string, len(allIDs)*2)
	for _, id := range allIDs {
		attempt, readErr := store.SummaryAttempt(ctx, id)
		if readErr != nil || attempt.Status != "interrupted" || attempt.Draft != nil {
			t.Fatal("terminal attempt", id, attempt, readErr)
		}
		receipt, readErr := store.SummaryAttemptRecovery(ctx, id)
		if readErr != nil || receipt.AttemptID != id {
			t.Fatal("terminal receipt", id, receipt, readErr)
		}
		attemptBytes, _ := json.Marshal(attempt)
		receiptBytes, _ := json.Marshal(receipt)
		before["attempt:"+id], before["receipt:"+id] = string(attemptBytes), string(receiptBytes)
	}
	receipts, err := store.ListSummaryAttemptRecoveries(ctx, base.TaskID, "", 100)
	if err != nil || len(receipts) != len(allIDs) {
		t.Fatal("recovery receipt cardinality", len(receipts), err)
	}
	for range 3 {
		cursor, recovered, err = store.ReconcileSummaryAttemptsPage(ctx, "", 2, now.Add(time.Hour))
		if err != nil || cursor != "" || recovered != 0 {
			t.Fatal("settled replay", cursor, recovered, err)
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range allIDs {
		attempt, attemptErr := store.SummaryAttempt(ctx, id)
		receipt, receiptErr := store.SummaryAttemptRecovery(ctx, id)
		attemptBytes, _ := json.Marshal(attempt)
		receiptBytes, _ := json.Marshal(receipt)
		if attemptErr != nil || receiptErr != nil || before["attempt:"+id] != string(attemptBytes) || before["receipt:"+id] != string(receiptBytes) {
			t.Fatal("reopen changed bytes", id, attemptErr, receiptErr)
		}
	}
}

func TestSummaryRecoveryRejectsActiveOwnerAndConcurrentSweep(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "active-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	attempt, _ := summaryAttemptFixture(t, store)
	if err = store.BeginSummary(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan error, workers)
	for range workers {
		go func() {
			_, recovered, reconcileErr := store.ReconcileSummaryAttemptsPage(ctx, "", 100, time.Unix(400, 0))
			if reconcileErr == nil && recovered != 0 {
				reconcileErr = errors.New("active owner recovered")
			}
			results <- reconcileErr
		}()
	}
	for range workers {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.SummaryAttempt(ctx, attempt.ID)
	if err != nil || current.Status != "started" {
		t.Fatal("active attempt changed", current, err)
	}
	if _, err = store.SummaryAttemptRecovery(ctx, attempt.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("active attempt has receipt", err)
	}
}
