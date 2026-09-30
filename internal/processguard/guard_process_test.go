//go:build darwin || linux

package processguard_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
)

// Every mutable guard belongs to a separate process. Tests never invalidate the
// parent test runner's singleton or rely on graceful owner cleanup.
func TestProcessGuardOwnedChild(t *testing.T) {
	if os.Getenv("DARWIN_PROCESS_GUARD_TEST_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refs := make(chan processguard.Reference, 16)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { ref, err := processguard.Current(ctx); refs <- ref; errs <- err })
	}
	wg.Wait()
	close(refs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("current guard unavailable", err)
		}
	}
	var first processguard.Reference
	for ref := range refs {
		if first.Version == 0 {
			first = ref
		}
		if ref != first || ref.Validate() != nil {
			t.Fatal("concurrent current identity changed")
		}
	}
	// First successful use freezes the owner even if later configuration changes.
	if err := os.Setenv("DARWIN_PROCESS_OWNER_DIR", filepath.Join(first.Directory, "unused-replacement")); err != nil {
		t.Fatal(err)
	}
	if ref, err := processguard.Current(ctx); err != nil || ref != first {
		t.Fatal("environment change rotated lifetime guard", err)
	}
	// Closing an observation must never release the process-lifetime owner.
	for range 2 {
		observation, err := processguard.Probe(ctx, first)
		if err != nil || observation == nil || observation.State != processguard.Held {
			t.Fatal("self guard not held", err)
		}
		if err = observation.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("DARWIN_PROCESS_GUARD_TEST_DAMAGE") == "1" {
		if err := os.Chmod(filepath.Join(first.Directory, "owner.lock"), 0644); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if ref, err := processguard.Current(ctx); err == nil || ref.Version != 0 {
				t.Fatal("damaged owner silently repaired")
			}
		}
		parent := filepath.Dir(first.Directory)
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 1 || filepath.Join(parent, entries[0].Name()) != first.Directory {
			t.Fatal("damaged owner created replacement directory", err)
		}
	}
	if os.Getenv("DARWIN_PROCESS_GUARD_TEST_EXEC") == "1" {
		body, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		env := append(os.Environ(), "DARWIN_PROCESS_GUARD_EXEC_REFERENCE="+string(body))
		if err := syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestProcessGuardExecChild$", "-test.timeout=10s"}, env); err != nil {
			t.Fatal(err)
		}
		t.Fatal("exec unexpectedly returned")
	}
	if err := json.NewEncoder(os.Stdout).Encode(first); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestProcessGuardExecChild(t *testing.T) {
	body := os.Getenv("DARWIN_PROCESS_GUARD_EXEC_REFERENCE")
	if body == "" {
		return
	}
	var ref processguard.Reference
	if json.Unmarshal([]byte(body), &ref) != nil || ref.Validate() != nil {
		t.Fatal("invalid exec fixture reference")
	}
	if err := json.NewEncoder(os.Stdout).Encode(ref); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type ownedGuard struct {
	ref     processguard.Reference
	command *exec.Cmd
	input   io.WriteCloser
	waited  bool
}

func startOwnedGuard(t *testing.T, mode ...string) *ownedGuard {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX lock qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessGuardOwnedChild$", "-test.timeout=10s")
	privateTemp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TMPDIR=") && !strings.HasPrefix(value, "DARWIN_PROCESS_OWNER_DIR=") && !strings.HasPrefix(value, "DARWIN_PROCESS_GUARD_TEST_CHILD=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "DARWIN_PROCESS_GUARD_TEST_CHILD=1", "TMPDIR="+privateTemp, "DARWIN_PROCESS_OWNER_DIR="+filepath.Join(privateTemp, "owners"))
	if len(mode) > 0 && mode[0] == "exec" {
		command.Env = append(command.Env, "DARWIN_PROCESS_GUARD_TEST_EXEC=1")
	}
	if len(mode) > 0 && mode[0] == "damage" {
		command.Env = append(command.Env, "DARWIN_PROCESS_GUARD_TEST_DAMAGE=1")
	}
	command.WaitDelay = time.Second
	command.Stderr = io.Discard
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	child := &ownedGuard{command: command, input: input}
	t.Cleanup(func() {
		_ = input.Close()
		if !child.waited {
			_ = command.Process.Kill()
			_ = command.Wait()
			child.waited = true
		}
		cancel()
	})
	lines := make(chan string, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 1024), 16384)
		if scanner.Scan() {
			lines <- scanner.Text()
		} else {
			lines <- ""
		}
	}()
	t.Cleanup(func() { _ = output.Close(); <-joined })
	var line string
	select {
	case line = <-lines:
	case <-ctx.Done():
		t.Fatal("guard child did not acknowledge")
	}
	if line == "" || json.Unmarshal([]byte(line), &child.ref) != nil || child.ref.Validate() != nil {
		t.Fatal("invalid child guard acknowledgement")
	}
	return child
}

func TestProcessGuardOwnerDescriptorClosesAcrossExec(t *testing.T) {
	child := startOwnedGuard(t, "exec")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := processguard.Probe(ctx, child.ref)
	if err != nil || observation == nil || observation.State != processguard.Unlocked {
		t.Fatal("owner descriptor survived exec", err)
	}
	if err = observation.Close(); err != nil {
		t.Fatal(err)
	}
	// The replacement image still runs with the same PID; this was exec's
	// CLOEXEC boundary, not accidental descendant exit or graceful cleanup.
	child.kill(t)
}

func TestProcessGuardCurrentDoesNotRepairDamagedLifetimeOwner(t *testing.T) {
	child := startOwnedGuard(t, "damage")
	observation, err := processguard.Probe(context.Background(), child.ref)
	if observation != nil {
		_ = observation.Close()
	}
	if err == nil {
		t.Fatal("damaged live owner reported observable")
	}
	child.kill(t)
}

func (child *ownedGuard) kill(t *testing.T) {
	t.Helper()
	if child.waited {
		t.Fatal("child already joined")
	}
	if err := child.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := child.command.Wait()
	child.waited = true
	if err == nil || child.command.ProcessState == nil {
		t.Fatal("guard owner exited without SIGKILL")
	}
	state, ok := child.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Fatal("guard owner was not SIGKILLed")
	}
}

func TestProcessGuardHeldUntilVerifiedProcessDeath(t *testing.T) {
	child := startOwnedGuard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 2 {
		observation, err := processguard.Probe(ctx, child.ref)
		if err != nil || observation == nil || observation.State != processguard.Held {
			t.Fatal("live owner reported unlocked", err)
		}
		if err = observation.Close(); err != nil {
			t.Fatal(err)
		}
	}
	child.kill(t)
	observation, err := processguard.Probe(ctx, child.ref)
	if err != nil || observation == nil || observation.State != processguard.Unlocked {
		t.Fatal("dead owner not provably unlocked", err)
	}
	if err = observation.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessGuardReferenceMetadataDoesNotContainCapabilities(t *testing.T) {
	child := startOwnedGuard(t)
	body, err := json.Marshal(child.ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "token") || strings.Contains(string(body), "private_key") {
		t.Fatal("unexpected authority in reference")
	}
}
