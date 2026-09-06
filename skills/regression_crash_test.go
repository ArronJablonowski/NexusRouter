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

func TestRegressionOperationOwnedCommitChild(t *testing.T) {
	if os.Getenv("DARWIN_REGRESSION_COMMIT_CHILD") != "1" {
		return
	}
	var expected ActivationState
	if json.Unmarshal([]byte(os.Getenv("DARWIN_REGRESSION_COMMIT_STATE")), &expected) != nil || expected.Validate() != nil {
		t.Fatal("invalid child state")
	}
	store, err := Open(os.Getenv("DARWIN_REGRESSION_COMMIT_ROOT"), []string{expected.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := store.RevalidateAndRollbackOnce(ctx, "lost-wrapper-response", "crash-validator", expected, ValidatorFunc(func(context.Context, Version) (Evidence, error) { return failedRegressionProof(), nil }))
	if err != nil || result.Validate() != nil || !result.Result.RolledBack {
		t.Fatal("child operation failed", err)
	}
	// Test-only barrier, not a response carrying the operation result. The store
	// call has committed and returned; its wrapper has not delivered a response.
	if _, err = io.WriteString(os.Stdout, "committed-before-wrapper-response\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	t.Fatal("wrapper unexpectedly resumed without being killed")
}

// Qualifies process death after an acknowledged filesystem commit, not a crash
// during rename/fsync, power loss, or exactly-once callback execution.
func TestRegressionOperationCommittedBeforeResponseSurvivesOwnedKill(t *testing.T) {
	s, path, key, predecessor, active, expected := regressionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	original, err := s.Load(ctx, key, active)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegressionOperationOwnedCommitChild$", "-test.timeout=10s")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DARWIN_REGRESSION_COMMIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "DARWIN_REGRESSION_COMMIT_CHILD=1", "DARWIN_REGRESSION_COMMIT_ROOT="+path, "DARWIN_REGRESSION_COMMIT_STATE="+string(body))
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
	if err = cmd.Start(); err != nil {
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
		if line != "committed-before-wrapper-response" {
			t.Fatal("child did not reach committed boundary")
		}
	case <-ctx.Done():
		t.Fatal("child commit acknowledgement timed out")
	}
	if err = cmd.Process.Kill(); err != nil {
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
	store, err := Open(path, []string{key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAutomatic(true)
	o, err := store.RegressionOperation(ctx, key, "lost-wrapper-response")
	if err != nil || o.Validate() != nil || o.Expected != expected || o.After.Active != predecessor || !o.Result.RolledBack || o.Result.Evidence != failedRegressionProof() {
		t.Fatal(o, err)
	}
	current, err := store.ActivationState(ctx, key)
	if err != nil || current != o.After {
		t.Fatal(current, err)
	}
	before := activationCatalogBytes(t, path)
	calls := 0
	retry, err := store.RevalidateAndRollbackOnce(ctx, "lost-wrapper-response", "crash-validator", expected, ValidatorFunc(func(context.Context, Version) (Evidence, error) { calls++; return failedRegressionProof(), nil }))
	if err != nil || !reflect.DeepEqual(retry, o) || calls != 0 {
		t.Fatal(retry, err, calls)
	}
	assertActivationCatalogUnchanged(t, path, before)
	source, err := store.Load(ctx, key, active)
	if err != nil || !reflect.DeepEqual(source, original) {
		t.Fatal("source version changed", err)
	}
	history, err := store.History(ctx, key)
	if err != nil || len(history.Activations) != o.ActivationCount+1 || history.Active != predecessor {
		t.Fatal("duplicate or missing rollback", err)
	}
}
