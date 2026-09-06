//go:build darwin || linux

package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOutcomeRollbackOwnedCommitChild(t *testing.T) {
	if os.Getenv("DARWIN_OUTCOME_COMMIT_CHILD") != "1" {
		return
	}
	var expected ActivationState
	var selection ComparisonSelectionReport
	if json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_COMMIT_STATE")), &expected) != nil || expected.Validate() != nil || json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_COMMIT_SELECTION")), &selection) != nil || selection.Validate() != nil {
		t.Fatal("invalid child fixture")
	}
	store, err := Open(os.Getenv("DARWIN_OUTCOME_COMMIT_ROOT"), []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := store.OutcomeRollbackOnce(ctx, "lost-outcome-response", expected, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) { return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil })
	if err != nil || receipt.Validate() != nil || receipt.Decision != "rolled_back" {
		t.Fatal("child outcome failed", err)
	}
	// Test-only barrier: the filesystem operation returned, but the wrapper has
	// not delivered its receipt to the caller. No production pause hook exists.
	if _, err := io.WriteString(os.Stdout, "outcome-committed-before-wrapper-response\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	t.Fatal("wrapper unexpectedly resumed")
}

// This qualifies death after commit and before wrapper acknowledgement, not a
// kill during rename/fsync, power loss, or exactly-once selector execution.
func TestOutcomeRollbackCommittedBeforeResponseSurvivesOwnedKill(t *testing.T) {
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
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutcomeRollbackOwnedCommitChild$", "-test.timeout=10s")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DARWIN_OUTCOME_COMMIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "DARWIN_OUTCOME_COMMIT_CHILD=1", "DARWIN_OUTCOME_COMMIT_ROOT="+path, "DARWIN_OUTCOME_COMMIT_STATE="+string(stateBody), "DARWIN_OUTCOME_COMMIT_SELECTION="+string(selectionBody))
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
		if line != "outcome-committed-before-wrapper-response" {
			t.Fatal("child did not reach committed boundary")
		}
	case <-ctx.Done():
		t.Fatal("child commit timed out")
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
	receipt, err := store.OutcomeRollbackOperation(ctx, expected.Key, "lost-outcome-response")
	if err != nil || receipt.Validate() != nil || receipt.Expected != expected || receipt.Decision != "rolled_back" || receipt.After.Active != selection.Policy.Comparison.BaselineVersion || !reflect.DeepEqual(receipt.Selection, selection) {
		t.Fatal("committed receipt missing or changed", err)
	}
	current, err := store.ActivationState(ctx, expected.Key)
	if err != nil || current != receipt.After {
		t.Fatal("state and receipt differ", err)
	}
	before := activationCatalogBytes(t, path)
	calls := 0
	retry, err := store.OutcomeRollbackOnce(ctx, "lost-outcome-response", expected, selection.Policy, func(context.Context) (ComparisonSelectionReport, error) {
		calls++
		return ComparisonSelectionReport{}, ErrInvalid
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil })
	if err != nil || !reflect.DeepEqual(retry, receipt) || calls != 0 {
		t.Fatal("retry selected evidence again", err, calls)
	}
	assertActivationCatalogUnchanged(t, path, before)
	source, err := store.Load(ctx, expected.Key, expected.Active)
	if err != nil || !reflect.DeepEqual(source, original) {
		t.Fatal("source version changed", err)
	}
	history, err := store.History(ctx, expected.Key)
	if err != nil || len(history.Activations) != receipt.ActivationCount+1 || history.Active != receipt.After.Active {
		t.Fatal("duplicate or missing rollback", err)
	}
}
