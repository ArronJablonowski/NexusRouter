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

func TestOutcomeIntentOwnedSelectingChild(t *testing.T) {
	if os.Getenv("DARWIN_OUTCOME_INTENT_CHILD") != "1" {
		return
	}
	var expected ActivationState
	var policy ComparisonSelectionPolicy
	if json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_INTENT_STATE")), &expected) != nil || expected.Validate() != nil || json.Unmarshal([]byte(os.Getenv("DARWIN_OUTCOME_INTENT_POLICY")), &policy) != nil || policy.Validate() != nil {
		t.Fatal("invalid child fixture")
	}
	store, err := Open(os.Getenv("DARWIN_OUTCOME_INTENT_ROOT"), []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	store.SetOutcomeRollback(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = store.OutcomeRollbackOnceGuarded(ctx, "interrupted-selection", "configured", expected, policy, func(context.Context) (ComparisonSelectionReport, error) {
		// The host selector is now entered, proving the durable intent precedes
		// its work. This is not a result and contains no selection evidence.
		if _, err := io.WriteString(os.Stdout, "durable-intent-before-selector-result\n"); err != nil {
			return ComparisonSelectionReport{}, err
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return ComparisonSelectionReport{}, ErrInvalid
	}, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeRollbackIntent) error { return nil })
	t.Fatal("selector unexpectedly returned", err)
}

// Qualification is owned-process death after the durable claim and before a
// selector result, not a crash during rename/fsync or a power-loss guarantee.
func TestOutcomeIntentSurvivesOwnedKillWithoutReselection(t *testing.T) {
	s, path, expected, selection := outcomeRollbackFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	original, err := s.Load(ctx, expected.Key, expected.Active)
	if err != nil {
		t.Fatal(err)
	}
	stateBody, _ := json.Marshal(expected)
	policyBody, _ := json.Marshal(selection.Policy)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutcomeIntentOwnedSelectingChild$", "-test.timeout=10s")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DARWIN_OUTCOME_INTENT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "DARWIN_OUTCOME_INTENT_CHILD=1", "DARWIN_OUTCOME_INTENT_ROOT="+path, "DARWIN_OUTCOME_INTENT_STATE="+string(stateBody), "DARWIN_OUTCOME_INTENT_POLICY="+string(policyBody))
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
		if line != "durable-intent-before-selector-result" {
			t.Fatal("child did not reach selector boundary")
		}
	case <-ctx.Done():
		t.Fatal("selector boundary timed out")
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
	intent, err := store.OutcomeRollbackIntent(ctx, expected.Key, "interrupted-selection")
	if err != nil || intent.Validate() != nil || intent.Expected != expected || intent.Policy != selection.Policy || intent.ConfiguredModelID != "configured" {
		t.Fatal("durable intent missing or changed", err)
	}
	current, err := store.ActivationState(ctx, expected.Key)
	if err != nil || current != expected {
		t.Fatal("interrupted selection changed activation", err)
	}
	if _, err := store.OutcomeRollbackOperation(ctx, expected.Key, "interrupted-selection"); !errors.Is(err, ErrNotFound) {
		t.Fatal("interrupted selector invented receipt", err)
	}
	before := activationCatalogBytes(t, path)
	calls := 0
	for _, mode := range []string{"exact", "new-operation", "new-policy", "new-model"} {
		id, model, policy := "interrupted-selection", "configured", selection.Policy
		switch mode {
		case "new-operation":
			id = "second-selection"
		case "new-policy":
			policy.Comparison.MinDrop = .2
		case "new-model":
			model = "other"
		}
		_, err := store.OutcomeRollbackOnceGuarded(ctx, id, model, expected, policy, func(context.Context) (ComparisonSelectionReport, error) { calls++; return selection, nil }, func(context.Context, OutcomeRollbackReceipt) error { return nil }, func(context.Context, OutcomeRollbackIntent) error { return nil })
		want := ErrConflict
		if mode == "exact" {
			want = ErrOutcomePending
		}
		if !errors.Is(err, want) || calls != 0 {
			t.Fatal("interrupted claim permitted reselection", mode, err, calls)
		}
	}
	assertActivationCatalogUnchanged(t, path, before)
	source, err := store.Load(ctx, expected.Key, expected.Active)
	if err != nil || !reflect.DeepEqual(source, original) {
		t.Fatal("source changed", err)
	}
	history, err := store.History(ctx, expected.Key)
	if err != nil || len(history.Activations) != intent.ActivationCount || history.Active != expected.Active {
		t.Fatal("unexpected activation transition", err)
	}
}
