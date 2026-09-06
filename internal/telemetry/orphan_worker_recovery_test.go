//go:build darwin || linux

package telemetry

import (
	"bufio"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"modernc.org/sqlite"
)

// The owner writes through real production APIs and then parks. Only the
// parent test kills this exact subprocess; lease expiry is not the death proof.
func TestOrphanWorkerOwnerHelper(t *testing.T) {
	mode := os.Getenv("DARWIN_ORPHAN_WORKER")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, os.Getenv("DARWIN_ORPHAN_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	queuedSubmission(t, s, "orphan")
	claim := claimSubmission(t, s)
	seq := map[string]int64{}
	stamp := time.Now().Add(-time.Second)
	appendEvent := func(task string, kind runtime.Kind, data runtime.Data) {
		if kind == runtime.TaskStarted {
			data.SubmissionID = claim.Status.ID
		}
		if task == "child" && (kind == runtime.TaskStarted || kind == runtime.TurnStarted || kind == runtime.TurnCompleted || kind == runtime.EvaluationRecorded) {
			data.ProviderID, data.ModelID = "fixture", "model"
		}
		seq[task]++
		stamp = stamp.Add(time.Millisecond)
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-%d", task, seq[task]), TaskID: task, SessionID: "parent", CorrelationID: task, Sequence: seq[task], Time: stamp, Kind: kind, Data: data}
		if task == "work" {
			e.WorkerID = "owner"
		} else if kind != runtime.TaskStarted {
			e.TurnID, e.AttemptID = "turn", "attempt"
		}
		if task == "child" {
			e.SessionID = "child"
		}
		if err := s.AppendSubmission(ctx, seq[task]-1, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("parent", runtime.TaskStarted, runtime.Data{})
	appendEvent("parent", runtime.TurnStarted, runtime.Data{})
	appendEvent("parent", runtime.TurnCompleted, runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"question","validation":"text"}`)}}})
	parentDispatch := runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect}
	if strings.HasPrefix(mode, "no_child") {
		parentDispatch.ToolBehavior = runtime.BehaviorReadOnly
	}
	appendEvent("parent", runtime.ToolStarted, parentDispatch)
	appendEvent("work", runtime.TaskStarted, runtime.Data{ParentTaskID: "parent", DelegationOrigin: &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}})
	lease, err := s.AcquireLease(ctx, "work", "owner", "scope", mode == "writer", time.Now(), time.Minute)
	if err != nil || lease.Token == "" {
		t.Fatal(err)
	}
	if mode == "no_child_before_started" {
		fmt.Println("orphan-worker-ready")
		<-ctx.Done()
		t.Fatal("parent did not kill helper")
	}
	appendEvent("work", runtime.WorkerStarted, runtime.Data{})
	if mode == "no_child" || mode == "no_child_heartbeat" {
		if mode == "no_child_heartbeat" {
			appendEvent("work", runtime.WorkerHeartbeat, runtime.Data{})
		}
		fmt.Println("orphan-worker-ready")
		<-ctx.Done()
		t.Fatal("parent did not kill helper")
	}
	appendEvent("child", runtime.TaskStarted, runtime.Data{ParentTaskID: "work"})
	appendEvent("child", runtime.TurnStarted, runtime.Data{})
	readOnly := mode == "readonly_active" || mode == "readonly_complete"
	if readOnly {
		appendEvent("child", runtime.TurnCompleted, runtime.Data{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{}`)}}})
		appendEvent("child", runtime.ToolStarted, runtime.Data{ToolCallID: "read", ToolName: "read_file", ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.UncertainEffect})
		appendEvent("child", runtime.ToolCompleted, runtime.Data{ToolCallID: "read", ToolName: "read_file", ToolBehavior: runtime.BehaviorReadOnly, Effect: runtime.NoEffect, Text: "private file content"})
		// The next turn has its own identity, as emitted by the real agent loop.
		seq["child"]++
		stamp = stamp.Add(time.Millisecond)
		e := runtime.Event{Version: 1, ID: "child-6", TaskID: "child", SessionID: "child", CorrelationID: "child", Sequence: 6, Time: stamp, Kind: runtime.TurnStarted, TurnID: "next-turn", AttemptID: "next-attempt", Data: runtime.Data{ProviderID: "fixture", ModelID: "model"}}
		if err := s.AppendSubmission(ctx, 5, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
		if mode == "readonly_complete" {
			e.ID, e.Sequence, e.Kind, e.Time = "child-7", 7, runtime.TurnCompleted, stamp.Add(time.Millisecond)
			e.Data.Text, e.Data.FinishReason = "private candidate output", "stop"
			if err := s.AppendSubmission(ctx, 6, e, claim.Status.ID, claim.Token); err != nil {
				t.Fatal(err)
			}
		}
	}
	if mode != "interrupted" && !readOnly {
		data := runtime.Data{Text: "private candidate output", FinishReason: "stop"}
		if mode == "pending" || mode == "confirmed" || mode == "uncertain" {
			data = runtime.Data{ToolCalls: []providers.ToolCall{{ID: "effect", Name: "tool", Arguments: json.RawMessage(`{}`)}}}
		}
		appendEvent("child", runtime.TurnCompleted, data)
		if len(data.ToolCalls) > 0 {
			appendEvent("child", runtime.ToolStarted, runtime.Data{ToolCallID: "effect", ToolName: "tool", Effect: runtime.NoEffect})
			if mode != "pending" {
				effect := runtime.ConfirmedEffect
				if mode == "uncertain" {
					effect = runtime.UncertainEffect
				}
				appendEvent("child", runtime.ToolCompleted, runtime.Data{ToolCallID: "effect", ToolName: "tool", Effect: effect})
			}
		}
	}
	if mode != "active" && !readOnly {
		terminal := runtime.TaskCompleted
		if mode == "failed" || mode == "pending" || mode == "interrupted" || mode == "uncertain" {
			terminal = runtime.TaskFailed
		} else if mode == "canceled" {
			terminal = runtime.TaskCanceled
		}
		if terminal == runtime.TaskCompleted {
			yes := true
			appendEvent("child", runtime.EvaluationRecorded, runtime.Data{Accepted: &yes, Code: "deterministic.nonempty_text.v1"})
		}
		appendEvent("child", terminal, runtime.Data{})
	}
	fmt.Println("orphan-worker-ready")
	<-ctx.Done()
	t.Fatal("parent did not kill helper")
}

func startOrphanWorkerOwner(t *testing.T, mode string) (*Store, string, string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "orphan.db")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOrphanWorkerOwnerHelper$")
	cmd.Env = []string{"DARWIN_PROCESS_OWNER_DIR=" + filepath.Join(t.TempDir(), "owners"), "PATH=/usr/bin:/bin", "DARWIN_ORPHAN_WORKER=" + mode, "DARWIN_ORPHAN_DB=" + path}
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
		if value != "orphan-worker-ready" {
			t.Fatal("missing worker owner boundary", value)
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
	if err = s.db.QueryRow(`SELECT token FROM resource_leases WHERE task_id='work'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return s, path, token, kill
}

func orphanHistories(t *testing.T, s *Store) map[string][]runtime.Event {
	t.Helper()
	h := map[string][]runtime.Event{}
	for _, task := range []string{"parent", "work", "child"} {
		var err error
		h[task], err = s.Read(context.Background(), task, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func assertOrphanUnchanged(t *testing.T, s *Store, before map[string][]runtime.Event) {
	t.Helper()
	if !reflect.DeepEqual(before, orphanHistories(t, s)) {
		t.Fatal("rejected recovery changed source history")
	}
	var released, receipts int
	if err := s.db.QueryRow(`SELECT sum(released) FROM resource_leases`).Scan(&released); err != nil || released != 0 {
		t.Fatal("lease changed", released, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("receipt changed", receipts, err)
	}
}

func TestOrphanWorkerRecoveryAfterSIGKILL(t *testing.T) {
	for _, mode := range []string{"completed", "failed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, mode)
			ctx := context.Background()
			before := orphanHistories(t, s)
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
				t.Fatal("held owner reclaimed", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			kill()
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || !changed {
				t.Fatal("dead worker not recovered", changed, err)
			}
			after := orphanHistories(t, s)
			if !reflect.DeepEqual(before["parent"], after["parent"]) || !reflect.DeepEqual(before["child"], after["child"]) || len(after["work"]) != len(before["work"])+1 || !reflect.DeepEqual(before["work"], after["work"][:len(before["work"])]) {
				t.Fatal("source prefix or child changed")
			}
			last := after["work"][len(after["work"])-1]
			if last.Kind != runtime.TaskFailed || last.Data.Code != "worker_owner_interrupted" || last.WorkerID != "owner" || last.Data.Text != "" {
				t.Fatal("incorrect recovered terminal", last)
			}
			var body []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "orphan_worker_owner_unlocked") || strings.Contains(string(body), token) || strings.Contains(string(body), "private candidate") || strings.Contains(string(body), "darwin-owner-") {
				t.Fatal("invalid or sensitive receipt")
			}
			for i := 0; i < 2; i++ {
				if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err != nil || changed {
					t.Fatal("repeat recovery", changed, err)
				}
			}
			var repeated []byte
			if err := s.db.QueryRow(`SELECT body FROM lease_recoveries WHERE lease_token=?`, token).Scan(&repeated); err != nil || !reflect.DeepEqual(body, repeated) || !reflect.DeepEqual(after, orphanHistories(t, s)) {
				t.Fatal("repeat mutated recovery", err)
			}
			start := event("new-writer", 1, runtime.TaskStarted)
			start.TaskID, start.CorrelationID = "writer", "writer"
			if err := s.Append(ctx, 0, start); err != nil {
				t.Fatal(err)
			}
			lease, err := s.AcquireLease(ctx, "writer", "new-owner", "scope", true, time.Now(), time.Minute)
			if err != nil || lease.Token == "" {
				t.Fatal("writer remained blocked", err)
			}
			if err = s.ReleaseLease(ctx, lease.Token, lease.Owner); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOrphanWorkerRecoveryRejectsUnsafeCandidates(t *testing.T) {
	for _, mode := range []string{"writer", "pending", "uncertain", "confirmed", "interrupted", "legacy", "missing", "missing_origin", "wrong_origin", "wrong_owner", "extra_owner", "corrupt_head"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, mode)
			kill()
			ctx := context.Background()
			var err error
			switch mode {
			case "legacy":
				_, err = s.db.Exec(`UPDATE resource_leases SET process_id=NULL`)
			case "missing":
				candidate, e := readRecoveryLease(ctx, s.db, token)
				if e != nil {
					t.Fatal(e)
				}
				err = os.Remove(filepath.Join(candidate.Reference.Directory, "owner.lock"))
			case "missing_origin":
				_, err = s.db.Exec(`UPDATE events SET body=json_remove(body,'$.data.delegation_origin') WHERE task_id='work' AND sequence=1`)
			case "wrong_origin":
				_, err = s.db.Exec(`UPDATE events SET body=json_set(body,'$.data.delegation_origin.tool_call_id','other') WHERE task_id='work' AND sequence=1`)
			case "wrong_owner":
				_, err = s.db.Exec(`UPDATE resource_leases SET owner='other'`)
			case "extra_owner":
				_, err = s.db.Exec(`INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,released,process_id) SELECT 'other-token',task_id,'other-owner',scope,writer,expires,released,process_id FROM resource_leases WHERE token=?`, token)
			case "corrupt_head":
				_, err = s.db.Exec(`UPDATE task_heads SET sequence=999 WHERE task_id='child'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := orphanHistories(t, s)
			if changed, _ := s.RecoverOrphanWorker(ctx, token, time.Now()); changed {
				t.Fatal("unsafe recovery")
			}
			assertOrphanUnchanged(t, s, before)
		})
	}
}

func TestOrphanWorkerRecoveryBoundaryRollback(t *testing.T) {
	for _, mode := range []string{"guard_damage", "cancel", "reference_drift", "child_drift", "parent_drift"} {
		t.Run(mode, func(t *testing.T) {
			s, _, token, kill := startOrphanWorkerOwner(t, "completed")
			kill()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before := orphanHistories(t, s)
			candidate, err := readRecoveryLease(ctx, s.db, token)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "reference_drift":
				_, err = s.db.Exec(`CREATE TRIGGER drift_orphan BEFORE UPDATE OF released ON resource_leases BEGIN UPDATE lease_processes SET body='{}'; END`)
			case "child_drift", "parent_drift":
				task := "child"
				if mode == "parent_drift" {
					task = "parent"
				}
				_, err = s.db.Exec(`CREATE TRIGGER drift_orphan BEFORE UPDATE OF released ON resource_leases BEGIN UPDATE events SET body=json_set(body,'$.data.text','unexpected drift') WHERE task_id='` + task + `' AND sequence=1; END`)
			default:
				name := fmt.Sprintf("orphan_boundary_%d", recoveryBoundaryFunction.Add(1))
				err = sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
					if mode == "cancel" {
						cancel()
					} else if err := os.Remove(filepath.Join(candidate.Reference.Directory, "owner.lock")); err != nil {
						return nil, err
					}
					return int64(1), nil
				})
				if err == nil {
					_, err = s.db.Exec(`CREATE TRIGGER boundary_orphan BEFORE UPDATE OF released ON resource_leases BEGIN SELECT ` + name + `(); END`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
				t.Fatal("boundary accepted", changed, err)
			}
			assertOrphanUnchanged(t, s, before)
			if mode == "reference_drift" {
				after, err := readRecoveryLease(context.Background(), s.db, token)
				if err != nil || after != candidate {
					t.Fatal("reference drift committed", err)
				}
			}
		})
	}
}

func TestOrphanWorkerRecoveryRollbackAndConcurrency(t *testing.T) {
	s, path, token, kill := startOrphanWorkerOwner(t, "completed")
	kill()
	ctx := context.Background()
	before := orphanHistories(t, s)
	for _, table := range []string{"events", "resource_leases", "lease_recoveries"} {
		action := "INSERT"
		if table == "resource_leases" {
			action = "UPDATE OF released"
		}
		if _, err := s.db.Exec(`CREATE TRIGGER reject_orphan BEFORE ` + action + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
			t.Fatal(err)
		}
		if changed, err := s.RecoverOrphanWorker(ctx, token, time.Now()); err == nil || changed {
			t.Fatal("trigger did not roll back", table, changed, err)
		}
		assertOrphanUnchanged(t, s, before)
		if _, err := s.db.Exec(`DROP TRIGGER reject_orphan`); err != nil {
			t.Fatal(err)
		}
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := store.RecoverOrphanWorker(ctx, token, time.Now())
			results <- changed
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for changed := range results {
		if changed {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("nonexclusive recovery", winners)
	}
	var receipts int
	if err := s.db.QueryRow(`SELECT count(*) FROM lease_recoveries`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal(receipts, err)
	}
}
