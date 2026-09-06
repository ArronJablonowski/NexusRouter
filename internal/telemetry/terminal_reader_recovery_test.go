//go:build darwin || linux

package telemetry

import (
	"bufio"
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"modernc.org/sqlite"
)

func TestTerminalReaderOwnerHelper(t *testing.T) {
	mode := os.Getenv("DARWIN_TERMINAL_OWNER")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, os.Getenv("DARWIN_TERMINAL_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := event("start", 1, runtime.TaskStarted)
	start.CorrelationID = "task"
	start.WorkerID = "owner"
	if err = s.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireLease(ctx, "task", "owner", "scope", mode == "writer", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(1)
	appendEvent := func(kind runtime.Kind, data runtime.Data) {
		seq++
		e := event(fmt.Sprint("event-", seq), seq, kind)
		e.WorkerID = "owner"
		e.CorrelationID = "task"
		e.Data = data
		if kind == runtime.TurnStarted || kind == runtime.TurnCompleted || kind == runtime.ToolStarted || kind == runtime.ToolCompleted {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		if err := s.AppendWorker(ctx, seq-1, e, lease.Token, lease.Owner, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "pending" || mode == "uncertain" || mode == "interrupted" {
		appendEvent(runtime.TurnStarted, runtime.Data{})
		if mode != "interrupted" {
			appendEvent(runtime.TurnCompleted, runtime.Data{ToolCalls: []providers.ToolCall{{ID: "call", Name: "tool", Arguments: []byte(`{}`)}}})
			appendEvent(runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "tool", Effect: runtime.NoEffect})
			if mode == "uncertain" {
				appendEvent(runtime.ToolCompleted, runtime.Data{ToolCallID: "call", ToolName: "tool", Effect: runtime.UncertainEffect, Text: "private"})
			}
		}
		appendEvent(runtime.TaskFailed, runtime.Data{})
	} else if mode != "running" {
		appendEvent(runtime.TaskCompleted, runtime.Data{Text: "private output"})
	}
	fmt.Println("terminal-owner-ready")
	<-ctx.Done()
	t.Fatal("parent did not kill helper")
}

var recoveryBoundaryFunction atomic.Uint64

func TestTerminalReaderRecoveryBoundaryRollback(t *testing.T) {
	for _, mode := range []string{"guard_damage", "cancel", "reference_drift", "bad_expiry"} {
		t.Run(mode, func(t *testing.T) {
			s, token, kill := startTerminalReaderOwner(t, "completed")
			kill()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			candidate, err := readRecoveryLease(ctx, s.db, token)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "reference_drift" {
				_, err = s.db.Exec(`CREATE TRIGGER drift_recovery BEFORE UPDATE OF released ON resource_leases BEGIN UPDATE lease_processes SET body='{}'; END`)
			} else if mode == "bad_expiry" {
				_, err = s.db.Exec(`UPDATE resource_leases SET expires=9223372036854775807`)
			} else {
				name := fmt.Sprintf("recovery_boundary_%d", recoveryBoundaryFunction.Add(1))
				err = sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
					if mode == "cancel" {
						cancel()
					} else {
						if err := os.Remove(filepath.Join(candidate.Reference.Directory, "owner.lock")); err != nil {
							return nil, err
						}
					}
					return int64(1), nil
				})
				if err == nil {
					_, err = s.db.Exec(`CREATE TRIGGER boundary_recovery BEFORE UPDATE OF released ON resource_leases BEGIN SELECT ` + name + `(); END`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverTerminalReader(ctx, token, time.Now().UTC()); err == nil || changed {
				t.Fatal("boundary accepted", changed, err)
			}
			var count, released int
			if err = s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if count != 0 || released != 0 {
				t.Fatal("boundary partially committed", count, released)
			}
			if mode == "reference_drift" {
				current, err := readRecoveryLease(context.Background(), s.db, token)
				if err != nil || current != candidate {
					t.Fatal("metadata drift committed", err)
				}
			}
		})
	}
}

func TestTerminalReaderRecoveryUTCInputBounds(t *testing.T) {
	s, token, _ := startTerminalReaderOwner(t, "completed")
	for _, now := range []time.Time{
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600)),
		time.Date(2260, 12, 31, 23, 59, 59, 0, time.FixedZone("west", -3600)),
	} {
		if changed, err := s.RecoverTerminalReader(context.Background(), token, now); err != ErrLeaseRecovery || changed {
			t.Fatal("invalid UTC bounds", changed, err)
		}
		if _, n, err := s.RecoverTerminalReadersPage(context.Background(), "", 1, now); err != ErrLeaseRecovery || n != 0 {
			t.Fatal(n, err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func startTerminalReaderOwner(t *testing.T, mode string) (*Store, string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "terminal.db")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalReaderOwnerHelper$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "DARWIN_TERMINAL_OWNER=" + mode, "DARWIN_TERMINAL_DB=" + path}
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
		scan := bufio.NewScanner(pipe)
		scan.Buffer(make([]byte, 64), 1024)
		if scan.Scan() {
			line <- scan.Text()
		} else {
			line <- ""
		}
	}()
	var once sync.Once
	kill := func() {
		once.Do(func() {
			if err := cmd.Process.Kill(); err != nil {
				t.Error(err)
			}
			err := cmd.Wait()
			if err == nil || cmd.ProcessState == nil {
				t.Error("not killed")
			} else if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Error("not SIGKILL")
			}
			<-done
		})
	}
	t.Cleanup(kill)
	select {
	case value := <-line:
		if value != "terminal-owner-ready" {
			t.Fatal("missing owner boundary")
		}
	case <-ctx.Done():
		t.Fatal("owner timeout")
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var token string
	if err = s.db.QueryRow(`SELECT token FROM resource_leases WHERE task_id='task'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return s, token, kill
}

func TestTerminalReaderRecoveryAfterSIGKILL(t *testing.T) {
	s, token, kill := startTerminalReaderOwner(t, "completed")
	ctx := context.Background()
	now := time.Now().UTC()
	before, err := s.Read(ctx, "task", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.RecoverTerminalReader(ctx, token, now); err != nil || changed {
		t.Fatal("held reclaimed", changed, err)
	}
	kill()
	if changed, err := s.RecoverTerminalReader(ctx, token, now); err != nil || !changed {
		t.Fatal("dead terminal not reclaimed", changed, err)
	}
	for i := 0; i < 2; i++ {
		if changed, err := s.RecoverTerminalReader(ctx, token, now); err != nil || changed {
			t.Fatal("retry", changed, err)
		}
	}
	after, err := s.Read(ctx, "task", 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("journal changed", err)
	}
	var released, count int
	if err = s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released); err != nil || released != 1 {
		t.Fatal(released, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = s.db.Exec(`UPDATE lease_recoveries SET digest='forged'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverTerminalReader(ctx, token, now); err != ErrLeaseRecovery {
		t.Fatal("conflicting receipt", err)
	}
}

func TestTerminalReaderRecoveryDeclinesUnsafeCandidates(t *testing.T) {
	for _, mode := range []string{"writer", "running", "pending", "uncertain", "interrupted", "legacy", "missing", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			childMode := mode
			if mode == "legacy" || mode == "missing" || mode == "unknown" {
				childMode = "completed"
			}
			s, token, kill := startTerminalReaderOwner(t, childMode)
			ctx := context.Background()
			kill()
			if mode == "legacy" {
				if _, err := s.db.Exec(`UPDATE resource_leases SET process_id=NULL`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unknown" {
				token = "unknown"
			}
			if mode == "missing" {
				c, err := readRecoveryLease(ctx, s.db, token)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(c.Reference.Directory, "owner.lock")); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Read(ctx, "task", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverTerminalReader(ctx, token, time.Now().UTC()); err != nil || changed {
				t.Fatal(changed, err)
			}
			after, err := s.Read(ctx, "task", 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("journal changed")
			}
			var released, count int
			if err = s.db.QueryRow(`SELECT sum(released) FROM resource_leases`).Scan(&released); err != nil || released != 0 {
				t.Fatal(released, err)
			}
			if err = s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}

func TestTerminalReaderRecoveryRollbackAndConcurrency(t *testing.T) {
	s, token, kill := startTerminalReaderOwner(t, "completed")
	kill()
	ctx := context.Background()
	for _, table := range []string{"lease_recoveries", "resource_leases"} {
		action := "INSERT"
		if table == "resource_leases" {
			action = "UPDATE OF released"
		}
		if _, err := s.db.Exec(`CREATE TRIGGER reject_recovery BEFORE ` + action + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.RecoverTerminalReader(ctx, token, time.Now().UTC()); err == nil || changed {
			t.Fatal(changed, err)
		}
		var count, released int
		_ = s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&count)
		_ = s.db.QueryRow(`SELECT released FROM resource_leases WHERE token=?`, token).Scan(&released)
		if count != 0 || released != 0 {
			t.Fatal("partial recovery")
		}
		if _, err := s.db.Exec(`DROP TRIGGER reject_recovery`); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	var dbIndex int
	var dbName, dbPath string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&dbIndex, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			changed, err := store.RecoverTerminalReader(ctx, token, time.Now().UTC())
			if err != nil {
				t.Error(err)
			}
			results <- changed
		}(store)
	}
	wg.Wait()
	close(results)
	count := 0
	for changed := range results {
		if changed {
			count++
		}
	}
	if count != 1 {
		t.Fatal("multiple recovery", count)
	}
}
