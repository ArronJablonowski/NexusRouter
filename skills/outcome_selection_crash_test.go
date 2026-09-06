//go:build darwin || linux

package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOutcomeSelectionOwnedFinalGuardChild(t *testing.T) {
	if os.Getenv("DARWIN_OUTCOME_SELECTION_CHILD") != "1" {
		return
	}
	var expected ActivationState
	var selection ComparisonSelectionReport
	if json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_SELECTION_STATE")), &expected) != nil || expected.Validate() != nil || json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_SELECTION_REPORT")), &selection) != nil || selection.Validate() != nil {
		t.Fatal("invalid child fixture")
	}
	store, err := Open(os.Getenv("DARWIN_OUTCOME_SELECTION_ROOT"), []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = store.OutcomeRollbackOnceCheckpointed(ctx, "selected-before-final-commit", "", expected, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error {
		// Test-only barrier inside the final receipt guard, not an operation
		// acknowledgement. The selected evidence is durable; rollback is not.
		if _, err := io.WriteString(os.Stdout, "selected-before-final-receipt-commit\n"); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return ErrInvalid
	}, func(context.Context, OutcomeRollbackIntent) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	t.Fatal("final guard unexpectedly returned", err)
}

// Qualifies SIGKILL after selected evidence persistence, not interruption of
// rename/fsync, power loss, or the lifetime of externally executing callbacks.
func TestOutcomeSelectionSurvivesOwnedKillBeforeFinalCommit(t *testing.T) {
	s, path, expected, selection := outcomeRollbackFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	original, err := s.Load(ctx, expected.Key, expected.Active)
	if err != nil {
		t.Fatal(err)
	}
	stateBody, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	reportBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutcomeSelectionOwnedFinalGuardChild$", "-test.timeout=10s")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DARWIN_OUTCOME_SELECTION_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "DARWIN_OUTCOME_SELECTION_CHILD=1", "DARWIN_OUTCOME_SELECTION_ROOT="+path, "DARWIN_OUTCOME_SELECTION_STATE="+string(stateBody), "DARWIN_OUTCOME_SELECTION_REPORT="+string(reportBody))
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	lines := make(chan string, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 128), 1024)
		if scanner.Scan() {
			lines <- scanner.Text()
		} else {
			lines <- ""
		}
	}()
	defer func() { _ = output.Close(); <-joined }()
	select {
	case line := <-lines:
		if line != "selected-before-final-receipt-commit" {
			t.Fatal("child did not reach final guard")
		}
	case <-ctx.Done():
		t.Fatal("final guard timed out")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("child was not killed")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("child did not exit through SIGKILL")
	}
	store, err := Open(path, []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	current, err := store.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		t.Fatal("uncommitted final guard changed activation", err)
	}
	if _, err := store.OutcomeRollbackOperation(ctx, expected.Key, "selected-before-final-commit"); !errors.Is(err, ErrNotFound) {
		t.Fatal("uncommitted guard produced receipt", err)
	}
	checkpoint, err := store.OutcomeSelectionCheckpoint(ctx, expected.Key, "selected-before-final-commit")
	if err != nil || checkpoint.Validate() != nil || checkpoint.Intent.Expected != expected || !reflect.DeepEqual(checkpoint.Report, selection) {
		t.Fatal("selected checkpoint was not durable before final guard", err)
	}
	calls := 0
	receipt, err := store.OutcomeRollbackOnceCheckpointed(ctx, "selected-before-final-commit", "", expected, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		calls++
		return ComparisonSelectionReport{}, ErrInvalid
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeRollbackIntent) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || receipt.Validate() != nil || calls != 0 || receipt.Decision != "rolled_back" || !reflect.DeepEqual(receipt.Selection, selection) {
		t.Fatal("retry did not consume exact stored evidence", err, calls)
	}
	before := activationCatalogBytes(t, path)
	again, err := store.OutcomeRollbackOnceCheckpointed(ctx, "selected-before-final-commit", "", expected, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		calls++
		return ComparisonSelectionReport{}, ErrInvalid
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeRollbackIntent) error { return nil }, func(context.Context, OutcomeSelectionCheckpoint) error { return nil })
	if err != nil || calls != 0 || !reflect.DeepEqual(again, receipt) {
		t.Fatal("completed retry changed evidence", err, calls)
	}
	assertActivationCatalogUnchanged(t, path, before)
	source, err := store.Load(ctx, expected.Key, expected.Active)
	if err != nil || !reflect.DeepEqual(source, original) {
		t.Fatal("immutable version changed", err)
	}
	history, err := store.History(ctx, expected.Key)
	if err != nil || len(history.Activations) != receipt.ActivationCount+1 || history.Active != receipt.After.Active {
		t.Fatal("duplicate or missing rollback", err)
	}
}
