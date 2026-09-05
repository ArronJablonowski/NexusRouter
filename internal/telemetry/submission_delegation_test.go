package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func interruptedTree(t *testing.T, db *Store, claim submissions.Claim) {
	t.Helper()
	stamp := time.Now().Add(-time.Second)
	seq := map[string]int64{}
	appendEvent := func(task, session string, kind runtime.Kind, data runtime.Data) {
		seq[task]++
		stamp = stamp.Add(time.Millisecond)
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-event-%d", task, seq[task]), TaskID: task, SessionID: session, CorrelationID: task, Sequence: seq[task], Time: stamp, Kind: kind, Data: data}
		if task == "work" {
			e.WorkerID = "worker"
		} else if kind != runtime.TaskStarted {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		if err := db.AppendSubmission(context.Background(), seq[task]-1, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	start := runtime.Data{SubmissionID: claim.Status.ID, Privacy: "local_only", ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "question"}}}
	appendEvent("parent", "parent", runtime.TaskStarted, start)
	model := runtime.Data{ProviderID: "fixture", ModelID: "model"}
	appendEvent("parent", "parent", runtime.TurnStarted, model)
	turn := model
	turn.FinishReason = "tool_calls"
	turn.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"question","validation":"text"}`)}}
	appendEvent("parent", "parent", runtime.TurnCompleted, turn)
	appendEvent("parent", "parent", runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect})
	appendEvent("work", "parent", runtime.TaskStarted, runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: "parent", DelegationOrigin: &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}})
	appendEvent("work", "parent", runtime.WorkerStarted, runtime.Data{})
	childStart := start
	childStart.ParentTaskID = "work"
	appendEvent("child", "child", runtime.TaskStarted, childStart)
	appendEvent("child", "child", runtime.TurnStarted, model)
	answer := model
	answer.Text = "answer"
	answer.FinishReason = "stop"
	appendEvent("child", "child", runtime.TurnCompleted, answer)
	yes := true
	validation := model
	validation.Accepted = &yes
	validation.Code = "deterministic.nonempty_text.v1"
	appendEvent("child", "child", runtime.EvaluationRecorded, validation)
	appendEvent("child", "child", runtime.TaskCompleted, model)
	appendEvent("work", "parent", runtime.EvaluationRecorded, runtime.Data{Accepted: &yes, Code: "worker_validator"})
	appendEvent("work", "parent", runtime.WorkerCompleted, runtime.Data{Text: "answer"})
	appendEvent("work", "parent", runtime.TaskCompleted, runtime.Data{})
}

func TestInterruptedDelegationRecoveryAtomicAndFenced(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			interruptedTree(t, db, claim)
			if cancel {
				if _, err := db.CancelSubmission(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := db.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if cancel {
				want = "canceled"
			}
			if status.State != want || status.LeaseExpiresAt != nil || status.Result == nil || status.Result.TaskID != "parent" || status.Result.Text != "" {
				t.Fatal(status)
			}
			snapshot, err := db.TaskSnapshot(ctx, "parent")
			if err != nil || snapshot.State != want || snapshot.Sequence != 6 || len(snapshot.Pending) != 0 || snapshot.UncertainEffects {
				t.Fatal(snapshot, err)
			}
			if len(snapshot.Messages) != 3 || snapshot.Messages[2].Role != "tool" {
				t.Fatal(snapshot.Messages)
			}
			var result struct {
				WorkTaskID      string `json:"work_task_id"`
				ExecutionTaskID string `json:"execution_task_id"`
				Output          string `json:"untrusted_output"`
			}
			if json.Unmarshal([]byte(snapshot.Messages[2].Content), &result) != nil || result.WorkTaskID != "work" || result.ExecutionTaskID != "child" || result.Output != "answer" {
				t.Fatal(snapshot.Messages[2])
			}
			receipts, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil || len(receipts) != 1 || receipts[0].Reason != "interrupted_delegation" || receipts[0].Action != want {
				t.Fatal(receipts, err)
			}
			if ok, err = db.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); ok || err != nil {
				t.Fatal(ok, err)
			}
			if _, err = db.FinishSubmission(ctx, job.ID, claim.Token, "failed", "execution_failed", nil); !errors.Is(err, submissions.ErrLeaseLost) {
				t.Fatal(err)
			}
			e := runtime.Event{Version: 1, ID: "stale-append", TaskID: "parent", SessionID: "parent", CorrelationID: "parent", Sequence: 5, Time: time.Now(), Kind: runtime.TaskFailed, Data: runtime.Data{Code: "execution_failed"}}
			if err = db.AppendSubmission(ctx, 4, e, job.ID, claim.Token); err == nil {
				t.Fatal("stale owner appended")
			}
		})
	}
}

func TestInterruptedDelegationRecoveryGuardsAndRollback(t *testing.T) {
	for _, mode := range []string{"live", "config", "partial", "missing_origin", "wrong_origin", "continuation", "corrupt", "invalid_token", "receipt_failure", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "job")
			claim := claimSubmission(t, db)
			interruptedTree(t, db, claim)
			now := time.Now().Add(2 * time.Minute)
			config := submitDigest("config")
			switch mode {
			case "live":
				now = time.Now()
			case "config":
				config = submitDigest("other")
			case "partial":
				if _, err := db.db.Exec(`DELETE FROM events WHERE task_id='work' AND sequence=5`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.db.Exec(`UPDATE task_heads SET sequence=4,state='running' WHERE task_id='work'`); err != nil {
					t.Fatal(err)
				}
			case "missing_origin":
				if _, err := db.db.Exec(`UPDATE events SET body=json_remove(body,'$.data.delegation_origin') WHERE task_id='work' AND sequence=1`); err != nil {
					t.Fatal(err)
				}
			case "wrong_origin":
				if _, err := db.db.Exec(`UPDATE events SET body=json_set(body,'$.data.delegation_origin.tool_call_id','other') WHERE task_id='work' AND sequence=1`); err != nil {
					t.Fatal(err)
				}
			case "invalid_token":
				if _, err := db.db.Exec(`UPDATE submissions SET token=''`); err != nil {
					t.Fatal(err)
				}
			case "aggregate":
				if _, err := db.db.Exec(`UPDATE events SET body=json_set(body,'$.fixture_padding',printf('%.*c',?,'x')) WHERE sequence=1`, 3<<20); err != nil {
					t.Fatal(err)
				}
			case "continuation":
				if _, err := db.db.Exec(`UPDATE submissions SET request=json_set(request,'$.request.ContinueTaskID','other')`); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if _, err := db.db.Exec(`UPDATE task_heads SET sequence=999 WHERE task_id='child'`); err != nil {
					t.Fatal(err)
				}
			case "receipt_failure":
				if _, err := db.db.Exec(`CREATE TRIGGER reject_recovery BEFORE INSERT ON submission_recoveries BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := db.RecoverInterruptedDelegation(ctx, job.ID, config, now)
			if ok {
				t.Fatal("unsafe recovery")
			}
			if (mode == "continuation" || mode == "corrupt" || mode == "receipt_failure" || mode == "invalid_token" || mode == "aggregate") && err == nil {
				t.Fatal("missing integrity failure")
			}
			if (mode == "live" || mode == "config" || mode == "partial" || mode == "missing_origin" || mode == "wrong_origin") && err != nil {
				t.Fatal(err)
			}
			snapshot, err := db.TaskSnapshot(ctx, "parent")
			if err != nil || snapshot.Sequence != 4 || snapshot.State != "running" {
				t.Fatal(snapshot, err)
			}
			status, err := db.Submission(ctx, job.ID)
			if err != nil || status.State != "running" || status.Result != nil {
				t.Fatal(status, err)
			}
			h, err := db.RecoveryHistory(ctx, job.ID)
			if err != nil || len(h) != 0 {
				t.Fatal(h, err)
			}
		})
	}
}

func TestInterruptedDelegationConcurrentRecovery(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claim := claimSubmission(t, db)
	interruptedTree(t, db, claim)
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, store := range []*Store{db, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			results <- ok
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal(winners)
	}
	snap, err := sessions.Replay(ctx, db, "parent")
	if err != nil || snap.Sequence != 6 {
		t.Fatal(snap, err)
	}
}

func TestInterruptedDelegationExpiredOwnerCannotRaceAppend(t *testing.T) {
	db, path := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claim := claimSubmission(t, db)
	interruptedTree(t, db, claim)
	if _, err := db.db.Exec(`UPDATE submissions SET lease_expires_at=? WHERE id=?`, submissionTime(time.Now().Add(-time.Minute)), job.ID); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		<-start
		e := runtime.Event{Version: 1, ID: "stale-race", TaskID: "parent", SessionID: "parent", CorrelationID: "parent", Sequence: 5, Time: time.Now(), Kind: runtime.TaskFailed, Data: runtime.Data{Code: "execution_failed"}}
		appended <- other.AppendSubmission(ctx, 4, e, job.ID, claim.Token)
	}()
	close(start)
	if ok, err := db.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), time.Now()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := <-appended; err == nil {
		t.Fatal("expired owner appended")
	}
	snapshot, err := db.TaskSnapshot(ctx, "parent")
	if err != nil || snapshot.Sequence != 6 || snapshot.State != "failed" {
		t.Fatal(snapshot, err)
	}
}

func TestInterruptedDelegationParentCancellationWins(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	job := queuedSubmission(t, db, "job")
	claim := claimSubmission(t, db)
	interruptedTree(t, db, claim)
	if _, err := db.RequestCancellation(ctx, "parent"); err != nil {
		t.Fatal(err)
	}
	before, err := db.Submission(ctx, job.ID)
	if err != nil || before.CancelRequested {
		t.Fatal(before, err)
	}
	if ok, err := db.RecoverInterruptedDelegation(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	after, err := db.Submission(ctx, job.ID)
	if err != nil || after.State != "canceled" || after.ErrorCode != "canceled" || after.Result == nil || after.Result.Text != "" {
		t.Fatal(after, err)
	}
	snapshot, err := db.TaskSnapshot(ctx, "parent")
	if err != nil || snapshot.State != "canceled" || len(snapshot.Pending) != 0 {
		t.Fatal(snapshot, err)
	}
	history, err := db.RecoveryHistory(ctx, job.ID)
	if err != nil || len(history) != 1 || history[0].Reason != "interrupted_delegation" || history[0].Action != "canceled" {
		t.Fatal(history, err)
	}
}
