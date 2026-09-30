package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func claimedBranchForInterruptedRecovery(t *testing.T, db *Store, key string) (submissions.Status, submissions.Claim, submissions.BranchSourceFence) {
	t.Helper()
	ctx := context.Background()
	completedBranchSource(t, db, "branch-source", "local_only")
	fence, err := db.BranchSource(ctx, "branch-source")
	if err != nil {
		t.Fatal(err)
	}
	body, requestDigest := branchEnvelope(t, fence, true)
	queued, err := db.CreateBranchSubmission(ctx, submitDigest(key), requestDigest, submitDigest("config"), body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, submitDigest("config"), time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != queued.ID {
		t.Fatal(claim.Status, err)
	}
	return queued, claim, fence
}

func appendInterruptedBranchModel(t *testing.T, db *Store, claim submissions.Claim, fence submissions.BranchSourceFence) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.ModelDelta} {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("branch-model-event-%d", i+1), TaskID: "branch-model", SessionID: fence.SessionID, CorrelationID: "branch-model", Sequence: int64(i + 1), Time: now.Add(time.Duration(i) * time.Millisecond), Kind: kind}
		if kind == runtime.TaskStarted {
			e.Data = runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy, ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "private branch prompt"}}}
		} else {
			e.TurnID, e.AttemptID = "turn", "attempt"
			e.Data.ProviderID, e.Data.ModelID = "fixture", "model"
		}
		if kind == runtime.ModelDelta {
			e.Data.Text = "private partial output"
		}
		if err := db.AppendSubmission(context.Background(), int64(i), e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
}

func appendInterruptedBranchDelegation(t *testing.T, db *Store, claim submissions.Claim, fence submissions.BranchSourceFence) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	sequence := map[string]int64{}
	appendEvent := func(task string, kind runtime.Kind, data runtime.Data) {
		sequence[task]++
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("%s-event-%d", task, sequence[task]), TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: sequence[task], Time: now.Add(time.Duration(len(sequence)+int(sequence[task])) * time.Millisecond), Kind: kind, Data: data}
		if task == "branch-work" {
			e.WorkerID = "worker"
		} else if kind != runtime.TaskStarted {
			e.TurnID, e.AttemptID = "turn", "attempt"
		}
		if err := db.AppendSubmission(context.Background(), sequence[task]-1, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	model := runtime.Data{ProviderID: "fixture", ModelID: "model"}
	root := runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy, ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "branch question"}}}
	appendEvent("branch-parent", runtime.TaskStarted, root)
	appendEvent("branch-parent", runtime.TurnStarted, model)
	turn := model
	turn.FinishReason = "tool_calls"
	turn.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"question","validation":"text"}`)}}
	appendEvent("branch-parent", runtime.TurnCompleted, turn)
	appendEvent("branch-parent", runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect})
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	appendEvent("branch-work", runtime.TaskStarted, runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: "branch-parent", DelegationOrigin: origin})
	appendEvent("branch-work", runtime.WorkerStarted, runtime.Data{})
	child := root
	child.ParentTaskID = "branch-work"
	appendEvent("branch-child", runtime.TaskStarted, child)
	appendEvent("branch-child", runtime.TurnStarted, model)
	answer := model
	answer.Text, answer.FinishReason = "answer", "stop"
	appendEvent("branch-child", runtime.TurnCompleted, answer)
	yes := true
	accepted := model
	accepted.Accepted, accepted.Code = &yes, "deterministic.nonempty_text.v1"
	appendEvent("branch-child", runtime.EvaluationRecorded, accepted)
	appendEvent("branch-child", runtime.TaskCompleted, model)
	appendEvent("branch-work", runtime.EvaluationRecorded, runtime.Data{Accepted: &yes, Code: "worker_validator"})
	appendEvent("branch-work", runtime.WorkerCompleted, runtime.Data{Text: "answer"})
	appendEvent("branch-work", runtime.TaskCompleted, runtime.Data{})
}

func TestBranchInterruptedModelAndDelegationRecoverWithExactFence(t *testing.T) {
	for _, path := range []string{"model", "delegation"} {
		t.Run(path, func(t *testing.T) {
			db, _ := submissionStore(t)
			queued, claim, fence := claimedBranchForInterruptedRecovery(t, db, "branch-"+path)
			var recover func(context.Context, string, string, time.Time) (bool, error)
			wantTask, wantSequence := "branch-model", int64(4)
			if path == "model" {
				appendInterruptedBranchModel(t, db, claim, fence)
				recover = db.RecoverInterruptedModel
			} else {
				appendInterruptedBranchDelegation(t, db, claim, fence)
				recover = db.RecoverInterruptedDelegation
				wantTask, wantSequence = "branch-parent", 6
			}
			ok, err := recover(context.Background(), queued.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			status, err := db.Submission(context.Background(), queued.ID)
			if err != nil || status.State != "failed" || status.Result == nil || status.Result.TaskID != wantTask || status.Result.Text != "" {
				t.Fatal(status, err)
			}
			page, err := db.ReadEventPage(context.Background(), wantTask, 0, 10)
			if err != nil || page.State != "failed" || page.HeadSequence != wantSequence || page.SessionID != fence.SessionID || page.Events[0].Data.ParentTaskID != fence.TaskID {
				t.Fatal(page, err)
			}
			history, err := db.RecoveryHistory(context.Background(), queued.ID)
			if err != nil || len(history) != 1 || history[0].Reason != "interrupted_"+path {
				t.Fatal(history, err)
			}
		})
	}
}

func TestBranchInterruptedRecoveryRejectsFenceCorruption(t *testing.T) {
	for _, path := range []string{"model", "delegation"} {
		for _, corruption := range []string{"removed", "mutated"} {
			t.Run(path+"/"+corruption, func(t *testing.T) {
				db, _ := submissionStore(t)
				queued, claim, fence := claimedBranchForInterruptedRecovery(t, db, "corrupt-"+path+"-"+corruption)
				var recover func(context.Context, string, string, time.Time) (bool, error)
				root := "branch-model"
				if path == "model" {
					appendInterruptedBranchModel(t, db, claim, fence)
					recover = db.RecoverInterruptedModel
				} else {
					appendInterruptedBranchDelegation(t, db, claim, fence)
					recover = db.RecoverInterruptedDelegation
					root = "branch-parent"
				}
				query := `UPDATE submissions SET request=json_remove(request,'$.branch') WHERE id=?`
				if corruption == "mutated" {
					query = `UPDATE submissions SET request=json_set(request,'$.branch.head_event_id','different-terminal') WHERE id=?`
				}
				if _, err := db.db.Exec(query, queued.ID); err != nil {
					t.Fatal(err)
				}
				// Keep the envelope digest internally consistent for mutation so
				// rejection proves the source fence itself is re-derived, rather
				// than merely detecting changed bytes.
				if corruption == "mutated" {
					var body []byte
					if err := db.db.QueryRow(`SELECT request FROM submissions WHERE id=?`, queued.ID).Scan(&body); err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256(body)
					if _, err := db.db.Exec(`UPDATE submissions SET request_digest=? WHERE id=?`, hex.EncodeToString(digest[:]), queued.ID); err != nil {
						t.Fatal(err)
					}
				}
				before, err := db.ReadEventPage(context.Background(), root, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				ok, recoverErr := recover(context.Background(), queued.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
				if ok || !errors.Is(recoverErr, submissions.ErrInvalid) {
					t.Fatal("corrupt branch fence recovered", ok, recoverErr)
				}
				after, err := db.ReadEventPage(context.Background(), root, 0, 100)
				if err != nil || after.HeadSequence != before.HeadSequence || after.State != "running" {
					t.Fatal("failed recovery changed task", before, after, err)
				}
				status, err := db.Submission(context.Background(), queued.ID)
				if err != nil || status.State != "running" || status.Result != nil {
					t.Fatal(status, err)
				}
				history, err := db.RecoveryHistory(context.Background(), queued.ID)
				if err != nil || len(history) != 0 {
					t.Fatal(history, err)
				}
			})
		}
	}
}
