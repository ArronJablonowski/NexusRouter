//go:build darwin || linux

package releasepack

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNativeGateCancellationTerminatesDescendantTree(t *testing.T) {
	for name, contextForTest := range map[string]func() (context.Context, func()){
		"canceled": func() (context.Context, func()) {
			return context.WithCancel(context.Background())
		},
		"deadline": func() (context.Context, func()) {
			ctx := newTriggeredDeadlineContext()
			return ctx, ctx.expire
		},
	} {
		t.Run(name, func(t *testing.T) {
			bin, state := t.TempDir(), t.TempDir()
			ready := filepath.Join(state, "ready")
			script := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s' \"$child\" > \"$NATIVE_READY\"\nwhile :; do sleep 1; done\n"
			if err := os.WriteFile(filepath.Join(bin, "make"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, stop := contextForTest()
			defer stop()
			result := make(chan error, 1)
			source := t.TempDir()
			env := append(environment(), "NATIVE_READY="+ready)
			go func() { result <- nativeGateCommand(ctx, source, env, "check", io.Discard) }()
			childPID := waitForNativeChild(t, ready)
			stop()
			err := <-result
			want := context.Canceled
			if name == "deadline" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("gate returned %v, want %v", err, want)
			}
			waitForNativeChildExit(t, childPID)
		})
	}
}

// triggeredDeadlineContext lets the test expire a deadline only after the
// helper process has confirmed startup. A short wall-clock timeout made this
// process-tree test dependent on host scheduling load rather than behavior.
type triggeredDeadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newTriggeredDeadlineContext() *triggeredDeadlineContext {
	return &triggeredDeadlineContext{done: make(chan struct{})}
}

func (*triggeredDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *triggeredDeadlineContext) Done() <-chan struct{}     { return c.done }
func (c *triggeredDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (*triggeredDeadlineContext) Value(any) any { return nil }
func (c *triggeredDeadlineContext) expire()     { c.once.Do(func() { close(c.done) }) }

func waitForNativeChild(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if parseErr == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native gate did not publish a live descendant")
	return 0
}

func waitForNativeChildExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("canceled gate descendant %d is still alive", pid)
}
