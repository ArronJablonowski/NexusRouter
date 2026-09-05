//go:build darwin || linux

package telemetry

import (
	"bufio"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"modernc.org/sqlite"
)

func crashWorkerTerminal() runtime.Event {
	e := event("finish", 2, runtime.TaskCompleted)
	e.WorkerID = "owner"
	e.CorrelationID = "task"
	return e
}

func TestWorkerFinishCrashHelper(t *testing.T) {
	mode := os.Getenv("DARWIN_FINISH_CRASH_MODE")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pause := func() { fmt.Println("finish-boundary"); <-ctx.Done() }
	if err := sqlite.RegisterScalarFunction("darwin_finish_pause", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		pause()
		return nil, errors.New("fixture was not killed")
	}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, os.Getenv("DARWIN_FINISH_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", false, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var trigger string
	switch mode {
	case "before_event":
		trigger = `CREATE TEMP TRIGGER crash_finish BEFORE INSERT ON events WHEN NEW.sequence=2 BEGIN SELECT darwin_finish_pause(); END`
	case "before_release":
		trigger = `CREATE TEMP TRIGGER crash_finish BEFORE UPDATE OF released ON resource_leases WHEN NEW.released=1 BEGIN SELECT darwin_finish_pause(); END`
	case "after_commit":
	default:
		t.Fatal("invalid fixture mode")
	}
	// The store owns one connection; a TEMP trigger remains private to this
	// child connection and cannot affect post-crash retry in the parent.
	if trigger != "" {
		if _, err = s.db.ExecContext(ctx, trigger); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.FinishLeased(ctx, 1, crashWorkerTerminal(), lease.Token, "owner"); err != nil {
		t.Fatal(err)
	}
	if mode == "after_commit" {
		pause()
	}
	t.Fatal("helper returned without abrupt termination")
}

func TestWorkerFinishSIGKILLAtomicity(t *testing.T) {
	for _, mode := range []string{"before_event", "before_release", "after_commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "finish-crash.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			start := event("start", 1, runtime.TaskStarted)
			start.CorrelationID, start.WorkerID = "task", "owner"
			if err := s.Append(ctx, 0, start); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerFinishCrashHelper$")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_FINISH_CRASH_MODE=" + mode, "DARWIN_FINISH_CRASH_DB=" + path}
			cmd.Stderr = io.Discard
			cmd.WaitDelay = time.Second
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line, done := make(chan string, 1), make(chan struct{})
			go func() {
				defer close(done)
				scanner := bufio.NewScanner(pipe)
				scanner.Buffer(make([]byte, 64), 1024)
				if scanner.Scan() {
					line <- scanner.Text()
				} else {
					line <- ""
				}
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				<-done
			}()
			select {
			case value := <-line:
				if value != "finish-boundary" {
					t.Fatal("missing crash boundary")
				}
			case <-ctx.Done():
				t.Fatal("crash boundary timed out")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || cmd.ProcessState == nil {
				t.Fatal("child exited gracefully")
			}
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("not SIGKILL")
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var lease Lease
			if err := reopened.db.QueryRowContext(ctx, `SELECT token FROM resource_leases WHERE task_id='task' AND owner='owner'`).Scan(&lease.Token); err != nil {
				t.Fatal(err)
			}
			before, err := reopened.ReadEventPage(ctx, "task", 0, 100)
			if err != nil || before.HasMore {
				t.Fatal(before, err)
			}
			var released int
			if err = reopened.db.QueryRowContext(ctx, `SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if mode == "after_commit" {
				if before.State != "completed" || before.HeadSequence != 2 || len(before.Events) != 2 || released != 1 {
					t.Fatal("commit not atomic", before, released)
				}
				for i := 0; i < 2; i++ {
					if err = reopened.FinishLeased(ctx, 1, crashWorkerTerminal(), lease.Token, "owner"); err != nil {
						t.Fatal("acknowledgement", err)
					}
				}
				after, err := reopened.ReadEventPage(ctx, "task", 0, 100)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("retry changed journal", err)
				}
				if err := reopened.db.QueryRowContext(ctx, `SELECT released FROM resource_leases WHERE token=?`, lease.Token).Scan(&released); err != nil || released != 1 {
					t.Fatal("retry changed release", released, err)
				}
			} else {
				if before.State != "running" || before.HeadSequence != 1 || len(before.Events) != 1 || released != 0 {
					t.Fatal("rollback not atomic", before, released)
				}
				if _, err = reopened.AcquireLease(ctx, "task", "other", "scope", true, time.Now(), time.Minute); err != ErrLeaseBusy {
					t.Fatal("crash abandoned reader fencing", err)
				}
			}
		})
	}
}
