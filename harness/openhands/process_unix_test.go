//go:build darwin || linux

package openhands

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"
)

func TestProcessCancellationAndOrphanRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & wait")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	start := time.Now()
	if err := runProcess(command); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("cancellation did not bound process group", err)
	}
	// A leader that exits without joining its own child is not successful cleanup.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	orphan := exec.CommandContext(ctx2, "/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 & exit 0")
	orphan.Stdout = io.Discard
	orphan.Stderr = io.Discard
	if err := runProcess(orphan); err == nil {
		t.Fatal("orphaned process was accepted")
	}
}

func TestOutputLimitCancelsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := boundedOutput{limit: 3, cancel: cancel}
	if _, err := output.Write([]byte("four")); err == nil || ctx.Err() == nil || len(output.data) != 0 {
		t.Fatal("oversized stdout was accepted")
	}
}
