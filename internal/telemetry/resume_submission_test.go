package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func resumeEnvelope(t *testing.T, fence submissions.ResumeSourceFence, local bool) ([]byte, string) {
	t.Helper()
	request := resumeRequestProjection{Compaction: json.RawMessage("null"), Prompt: "resume prompt", ContinueTaskID: fence.TaskID, LocalRequired: local}
	body, err := json.Marshal(struct {
		Version int                            `json:"version"`
		Request any                            `json:"request"`
		Resume  *submissions.ResumeSourceFence `json:"resume,omitempty"`
	}{1, request, &fence})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:])
}

func resumeSourceBytes(t *testing.T, db *Store, task string) string {
	t.Helper()
	var body string
	if err := db.db.QueryRow(`SELECT group_concat(CAST(body AS TEXT),'') FROM (SELECT body FROM events WHERE task_id=? ORDER BY sequence)`, task).Scan(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func createClaimedResume(t *testing.T, db *Store, key string) (submissions.Status, submissions.Claim, submissions.ResumeSourceFence, []byte, string) {
	t.Helper()
	fence := recoveredModelSource(t, db, "local_only")
	body, digest := resumeEnvelope(t, fence, true)
	config := submitDigest("config")
	created, err := db.CreateResumeSubmission(context.Background(), submitDigest(key), digest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(context.Background(), config, time.Now(), time.Minute)
	if err != nil || claim.Status.ID != created.ID {
		t.Fatal(claim.Status, err)
	}
	return created, claim, fence, body, config
}

func appendInterruptedResumeModel(t *testing.T, db *Store, claim submissions.Claim, fence submissions.ResumeSourceFence) string {
	t.Helper()
	task := "resume-model"
	now := time.Now().Add(-time.Second)
	events := []runtime.Event{
		{Version: 1, ID: task + "-start", TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy, ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "resume"}}}},
		{Version: 1, ID: task + "-turn", TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: 2, Time: now.Add(time.Millisecond), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "fixture", ModelID: "model"}},
		{Version: 1, ID: task + "-delta", TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: 3, Time: now.Add(2 * time.Millisecond), Kind: runtime.ModelDelta, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "fixture", ModelID: "model", Text: "partial"}},
	}
	for i, event := range events {
		if err := db.AppendSubmission(context.Background(), int64(i), event, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func appendInterruptedResumeDelegation(t *testing.T, db *Store, claim submissions.Claim, fence submissions.ResumeSourceFence) string {
	t.Helper()
	root, work, child := "resume-parent", "resume-work", "resume-execution"
	stamp := time.Now().Add(-time.Second)
	seq := map[string]int64{}
	appendEvent := func(task string, kind runtime.Kind, data runtime.Data) {
		seq[task]++
		stamp = stamp.Add(time.Millisecond)
		e := runtime.Event{Version: 1, ID: task + "-event-" + string(rune('0'+seq[task])), TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: seq[task], Time: stamp, Kind: kind, Data: data}
		if task == work {
			e.WorkerID = "worker"
		} else if kind != runtime.TaskStarted {
			e.TurnID, e.AttemptID = "turn", "attempt"
		}
		if err := db.AppendSubmission(context.Background(), seq[task]-1, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	start := runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy, ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "resume"}}}
	appendEvent(root, runtime.TaskStarted, start)
	model := runtime.Data{ProviderID: "fixture", ModelID: "model"}
	appendEvent(root, runtime.TurnStarted, model)
	turn := model
	turn.FinishReason = "tool_calls"
	turn.ToolCalls = []providers.ToolCall{{ID: "call", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"question","validation":"text"}`)}}
	appendEvent(root, runtime.TurnCompleted, turn)
	appendEvent(root, runtime.ToolStarted, runtime.Data{ToolCallID: "call", ToolName: "delegate", Effect: runtime.NoEffect})
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	appendEvent(work, runtime.TaskStarted, runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: root, Privacy: fence.EffectivePrivacy, DelegationOrigin: origin})
	appendEvent(work, runtime.WorkerStarted, runtime.Data{})
	childStart := runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: work, Privacy: fence.EffectivePrivacy, ProviderID: "fixture", ModelID: "model", Messages: []providers.Message{{Role: "user", Content: "question"}}}
	appendEvent(child, runtime.TaskStarted, childStart)
	appendEvent(child, runtime.TurnStarted, model)
	answer := model
	answer.Text, answer.FinishReason = "answer", "stop"
	appendEvent(child, runtime.TurnCompleted, answer)
	yes := true
	appendEvent(child, runtime.EvaluationRecorded, runtime.Data{ProviderID: "fixture", ModelID: "model", Accepted: &yes, Code: "deterministic.nonempty_text.v1"})
	appendEvent(child, runtime.TaskCompleted, runtime.Data{ProviderID: "fixture", ModelID: "model"})
	appendEvent(work, runtime.EvaluationRecorded, runtime.Data{Accepted: &yes, Code: "worker_validator"})
	appendEvent(work, runtime.WorkerCompleted, runtime.Data{Text: "answer"})
	appendEvent(work, runtime.TaskCompleted, runtime.Data{})
	return root
}

func recoveredModelSource(t *testing.T, db *Store, privacy string) submissions.ResumeSourceFence {
	t.Helper()
	job := queuedSubmission(t, db, "recovered-model-source")
	claim := claimSubmission(t, db)
	events := interruptedModelFixture(t, db, claim, "")
	if privacy != "cloud_allowed" {
		start := events[0]
		start.Data.Privacy = privacy
		body, err := start.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.db.Exec(`UPDATE events SET body=? WHERE task_id=? AND sequence=1`, body, start.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := db.RecoverInterruptedModel(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	fence, err := db.ResumeSource(context.Background(), "model-task")
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func TestResumeSubmissionAtomicIdempotencyAndStartFence(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	fence := recoveredModelSource(t, db, "cloud_allowed")
	if fence.Validate() != nil || fence.SourceState != "failed" || fence.RecoveryReason != "recovered_model" || fence.EffectivePrivacy != "cloud_allowed" {
		t.Fatal(fence)
	}
	body, requestDigest := resumeEnvelope(t, fence, false)
	key, config := submitDigest("resume-key"), submitDigest("config")
	created, err := db.CreateResumeSubmission(ctx, key, requestDigest, config, body)
	if err != nil || created.State != "queued" {
		t.Fatal(created, err)
	}
	again, err := db.CreateResumeSubmission(ctx, key, requestDigest, config, body)
	if err != nil || again.ID != created.ID {
		t.Fatal(again, err)
	}
	if _, err = db.CreateSubmission(ctx, key, requestDigest, config, body); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("generic submission admitted resume envelope", err)
	}
	changed := fence
	changed.RecoveryReason = "recovered_delegation"
	changedBody, changedDigest := resumeEnvelope(t, changed, false)
	if _, err = db.CreateResumeSubmission(ctx, key, changedDigest, config, changedBody); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("changed idempotent resume did not conflict", err)
	}
	if _, err = db.CreateResumeSubmission(ctx, submitDigest("wrong-recovery-reason"), changedDigest, config, changedBody); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("mismatched recovery reason admitted", err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now(), time.Minute)
	if err != nil || claim.Status.ID != created.ID || db.ValidateResumeSubmission(ctx, created.ID) != nil {
		t.Fatal(claim.Status, err)
	}
	child := runtime.Event{Version: 1, ID: "resume-child-start", TaskID: "resume-child", SessionID: fence.SessionID, CorrelationID: "resume-child", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: "local_only", ProviderID: "fixture", ModelID: "model"}}
	if err = db.AppendSubmission(ctx, 0, child, created.ID, claim.Token); err != nil {
		t.Fatal("valid stricter resume start rejected", err)
	}
	turn := runtime.Event{Version: 1, ID: "resume-child-turn", TaskID: child.TaskID, SessionID: child.SessionID, CorrelationID: child.TaskID, Sequence: 2, Time: time.Now(), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "fixture", ModelID: "model"}}
	failed := runtime.Event{Version: 1, ID: "resume-child-failed", TaskID: child.TaskID, SessionID: child.SessionID, CorrelationID: child.TaskID, Sequence: 3, Time: time.Now(), Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Code: "provider_retryable_no_output"}}
	if err = db.AppendSubmission(ctx, 1, turn, created.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if err = db.AppendSubmission(ctx, 2, failed, created.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	fallback := runtime.Event{Version: 1, ID: "resume-fallback-start", TaskID: "resume-fallback", SessionID: fence.SessionID, CorrelationID: "resume-fallback", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, RetryOfTaskID: child.TaskID, Privacy: "local_only"}}
	if err = db.AppendSubmission(ctx, 0, fallback, created.ID, claim.Token); err != nil {
		t.Fatal("safe fallback rejected", err)
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	worker := runtime.Event{Version: 1, ID: "resume-worker-start", TaskID: "resume-worker", SessionID: fence.SessionID, CorrelationID: "resume-worker", WorkerID: "worker", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fallback.TaskID, DelegationOrigin: origin, Privacy: "local_only"}}
	if err = db.AppendSubmission(ctx, 0, worker, created.ID, claim.Token); err != nil {
		t.Fatal("scoped resume worker rejected", err)
	}
}

func TestResumeSubmissionEnforcesLocalPrivacyCeiling(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	fence := recoveredModelSource(t, db, "local_only")
	body, requestDigest := resumeEnvelope(t, fence, true)
	config := submitDigest("config")
	created, err := db.CreateResumeSubmission(ctx, submitDigest("local-resume"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now(), time.Minute)
	if err != nil || claim.Status.ID != created.ID {
		t.Fatal(claim.Status, err)
	}
	cloud := runtime.Event{Version: 1, ID: "cloud-resume-start", TaskID: "cloud-resume", SessionID: fence.SessionID, CorrelationID: "cloud-resume", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: "cloud_allowed"}}
	if err = db.AppendSubmission(ctx, 0, cloud, created.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("local resume escaped to cloud", err)
	}
}

func TestResumeSourceExactReasonsAndUnsafeStates(t *testing.T) {
	t.Run("delegation", func(t *testing.T) {
		db, _ := submissionStore(t)
		job := queuedSubmission(t, db, "recovered-delegation-source")
		claim := claimSubmission(t, db)
		interruptedTree(t, db, claim)
		if ok, err := db.RecoverInterruptedDelegation(context.Background(), job.ID, submitDigest("config"), time.Now().Add(2*time.Minute)); err != nil || !ok {
			t.Fatal(ok, err)
		}
		fence, err := db.ResumeSource(context.Background(), "parent")
		if err != nil || fence.RecoveryReason != "recovered_delegation" || fence.SourceState != "failed" || fence.Validate() != nil {
			t.Fatal(fence, err)
		}
	})
	t.Run("ordinary failure", func(t *testing.T) {
		db, _ := submissionStore(t)
		now := time.Now()
		start := runtime.Event{Version: 1, ID: "ordinary-start", TaskID: "ordinary", SessionID: "ordinary", CorrelationID: "ordinary", Sequence: 1, Time: now, Kind: runtime.TaskStarted}
		end := runtime.Event{Version: 1, ID: "ordinary-end", TaskID: "ordinary", SessionID: "ordinary", CorrelationID: "ordinary", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskFailed, Data: runtime.Data{Code: "execution_failed"}}
		if err := db.Append(context.Background(), 0, start); err != nil {
			t.Fatal(err)
		}
		if err := db.Append(context.Background(), 1, end); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ResumeSource(context.Background(), "ordinary"); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatal("ordinary failed task admitted", err)
		}
	})
}

func TestResumeSourceRejectsForgedRelationalLineage(t *testing.T) {
	for _, field := range []string{"parent_task_id", "retry_of_task_id"} {
		t.Run(field, func(t *testing.T) {
			db, _ := submissionStore(t)
			fence := recoveredModelSource(t, db, "local_only")
			var raw []byte
			if err := db.db.QueryRow(`SELECT body FROM events WHERE task_id=? AND sequence=1`, fence.TaskID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var start runtime.Event
			if json.Unmarshal(raw, &start) != nil {
				t.Fatal("invalid fixture")
			}
			if field == "parent_task_id" {
				start.Data.ParentTaskID = "missing-parent"
			} else {
				start.Data.RetryOfTaskID = "missing-retry"
			}
			body, err := start.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.db.Exec(`UPDATE events SET body=? WHERE task_id=? AND sequence=1`, body, fence.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.ResumeSource(context.Background(), fence.TaskID); !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal("forged resume lineage admitted", err)
			}
		})
	}
}

func TestResumeSourceRequiresDurableRecoveryReceipt(t *testing.T) {
	for _, mode := range []string{"missing", "reason", "action", "digest", "result", "error_code", "canceled", "lease", "prior_canceled"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			fence := recoveredModelSource(t, db, "local_only")
			var query string
			switch mode {
			case "missing":
				query = `DELETE FROM submission_recoveries`
			case "reason":
				query = `UPDATE submission_recoveries SET body=json_set(body,'$.reason','interrupted_delegation')`
			case "action":
				query = `UPDATE submission_recoveries SET body=json_set(body,'$.action','canceled')`
			case "digest":
				query = `UPDATE submission_recoveries SET prior_token_digest='BAD'`
			case "result":
				query = `UPDATE submissions SET result=CAST(result AS TEXT)||' '`
			case "error_code":
				query = `UPDATE submissions SET error_code='recovery_exhausted'`
			case "canceled":
				query = `UPDATE submissions SET cancel_requested=1`
			case "lease":
				query = `UPDATE submissions SET lease_expires_at='2099-01-01T00:00:00Z'`
			case "prior_canceled":
				var submissionID, recordID string
				var raw []byte
				if err := db.db.QueryRow(`SELECT json_extract(body,'$.data.submission_id') FROM events WHERE task_id=? AND sequence=1`, fence.TaskID).Scan(&submissionID); err != nil {
					t.Fatal(err)
				}
				if err := db.db.QueryRow(`SELECT id,body FROM submission_recoveries WHERE submission_id=?`, submissionID).Scan(&recordID, &raw); err != nil {
					t.Fatal(err)
				}
				var receipt submissions.Recovery
				if json.Unmarshal(raw, &receipt) != nil {
					t.Fatal("invalid recovery fixture")
				}
				terminalTime := receipt.Time
				receipt.Action, receipt.Reason, receipt.Time = "canceled", "cancellation_requested", terminalTime.Add(-time.Second)
				raw, _ = json.Marshal(receipt)
				if _, err := db.db.Exec(`UPDATE submission_recoveries SET body=? WHERE id=?`, raw, recordID); err != nil {
					t.Fatal(err)
				}
				terminal := submissions.Recovery{Version: 1, ID: "replacement-terminal", SubmissionID: submissionID, Time: terminalTime, Action: "failed", Reason: "interrupted_model"}
				raw, _ = json.Marshal(terminal)
				if _, err := db.db.Exec(`INSERT INTO submission_recoveries VALUES(?,?,?,?)`, terminal.ID, submissionID, submitDigest("replacement-token"), raw); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := db.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ResumeSource(context.Background(), fence.TaskID); !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal("synthetic recovered tail admitted without exact receipt", err)
			}
		})
	}
}

func TestResumeSubmissionRejectsPoisonRequestShapes(t *testing.T) {
	db, _ := submissionStore(t)
	fence := recoveredModelSource(t, db, "local_only")
	for _, request := range []any{
		map[string]any{"ContinueTaskID": fence.TaskID, "LocalRequired": true},
		map[string]any{"ContinueTaskID": fence.TaskID, "LocalRequired": true, "Prompt": "   "},
		map[string]any{"ContinueTaskID": fence.TaskID, "LocalRequired": true, "Prompt": "resume", "Messages": []any{}},
		map[string]any{"ContinueTaskID": fence.TaskID, "LocalRequired": true, "Prompt": "resume", "Compaction": map[string]any{}},
		map[string]any{"ContinueTaskID": fence.TaskID, "LocalRequired": true, "Prompt": "resume", "SummaryAttemptID": "summary"},
	} {
		body, err := json.Marshal(struct {
			Version int                            `json:"version"`
			Request any                            `json:"request"`
			Resume  *submissions.ResumeSourceFence `json:"resume"`
		}{1, request, &fence})
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		if _, err = db.CreateResumeSubmission(context.Background(), submitDigest(string(body)), hex.EncodeToString(digest[:]), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatal("poison resume request admitted", request, err)
		}
	}
}

func TestResumeSubmissionRejectsSourceDriftAtBoundariesAndRecovery(t *testing.T) {
	for _, boundary := range []string{"idempotency", "claim", "start", "recover_undispatched", "recover_terminal", "recover_model", "recover_delegation"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "resume.db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			fence := recoveredModelSource(t, db, "local_only")
			body, requestDigest := resumeEnvelope(t, fence, true)
			config := submitDigest("config")
			created, err := db.CreateResumeSubmission(ctx, submitDigest("resume-"+boundary), requestDigest, config, body)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := db.ClaimSubmission(ctx, config, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "start" && db.ValidateResumeSubmission(ctx, created.ID) != nil {
				t.Fatal("valid resume rejected before drift")
			}
			if _, err = db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id=? AND sequence=?`, fence.TaskID, fence.HeadSequence); err != nil {
				t.Fatal(err)
			}
			switch boundary {
			case "idempotency":
				if _, err = db.CreateResumeSubmission(ctx, submitDigest("resume-"+boundary), requestDigest, config, body); !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal(err)
				}
			case "claim":
				if err = db.ValidateResumeSubmission(ctx, created.ID); !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal(err)
				}
			case "start":
				child := runtime.Event{Version: 1, ID: "resume-start", TaskID: "resume-task", SessionID: fence.SessionID, CorrelationID: "resume-task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: "local_only"}}
				if err = db.AppendSubmission(ctx, 0, child, created.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
					t.Fatal(err)
				}
			case "recover_undispatched", "recover_terminal", "recover_model", "recover_delegation":
				var ok bool
				var recoverErr error
				switch boundary {
				case "recover_undispatched":
					ok, recoverErr = db.RecoverUndispatched(ctx, created.ID, config, time.Now().Add(2*time.Minute))
				case "recover_terminal":
					ok, recoverErr = db.RecoverTerminalSubmission(ctx, created.ID, config, time.Now().Add(2*time.Minute))
				case "recover_model":
					ok, recoverErr = db.RecoverInterruptedModel(ctx, created.ID, config, time.Now().Add(2*time.Minute))
				case "recover_delegation":
					ok, recoverErr = db.RecoverInterruptedDelegation(ctx, created.ID, config, time.Now().Add(2*time.Minute))
				}
				if ok || !errors.Is(recoverErr, submissions.ErrInvalid) {
					t.Fatal(ok, recoverErr)
				}
			}
		})
	}
}

func TestSubmissionControlAliasesFailClosedAtCreateAppendAndRecovery(t *testing.T) {
	controls := []string{
		`{"version":1,"request":{},"Resume":null}`,
		`{"version":1,"request":{},"re_sume":null}`,
		`{"version":1,"request":{},"resume":null,"Resume":null}`,
		`{"version":1,"request":{},"Branch":null}`,
		`{"version":1,"request":{},"br-anch":null}`,
	}
	for i, raw := range controls {
		t.Run(time.Duration(i).String(), func(t *testing.T) {
			ctx := context.Background()
			db, _ := submissionStore(t)
			body := []byte(raw)
			digest := submitDigest(raw)
			config := submitDigest("config")
			if _, err := db.CreateSubmission(ctx, submitDigest("alias-create"), digest, config, body); !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal("generic create admitted reserved control alias", err)
			}

			job := queuedSubmission(t, db, "alias-existing")
			if _, err := db.db.Exec(`UPDATE submissions SET request=?,request_digest=? WHERE id=?`, body, digest, job.ID); err != nil {
				t.Fatal(err)
			}
			claim := claimSubmission(t, db)
			start := runtime.Event{Version: 1, ID: "alias-start", TaskID: "alias-task", SessionID: "alias-session", CorrelationID: "alias-task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: job.ID}}
			if err := db.AppendSubmission(ctx, 0, start, job.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
				t.Fatal("append treated reserved control alias as generic", err)
			}
			if ok, err := db.RecoverUndispatched(ctx, job.ID, config, time.Now().Add(2*time.Minute)); ok || !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal("recovery treated reserved control alias as generic", ok, err)
			}
		})
	}
}

func TestResumeSubmissionPositiveRecoveryPathsAcrossReopen(t *testing.T) {
	for _, mode := range []string{"undispatched", "terminal", "model", "delegation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "resume-recovery.db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			created, claim, fence, _, config := createClaimedResume(t, db, "positive-"+mode)
			sourceBefore := resumeSourceBytes(t, db, fence.TaskID)
			root := ""
			switch mode {
			case "terminal":
				root = appendCompletedBranchTask(t, db, claim, resumeAsBranch(fence))
			case "model":
				root = appendInterruptedResumeModel(t, db, claim, fence)
			case "delegation":
				root = appendInterruptedResumeDelegation(t, db, claim, fence)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var recovered bool
			switch mode {
			case "undispatched":
				recovered, err = db.RecoverUndispatched(ctx, created.ID, config, time.Now().Add(2*time.Minute))
			case "terminal":
				recovered, err = db.RecoverTerminalSubmission(ctx, created.ID, config, time.Now().Add(2*time.Minute))
			case "model":
				recovered, err = db.RecoverInterruptedModel(ctx, created.ID, config, time.Now().Add(2*time.Minute))
			case "delegation":
				recovered, err = db.RecoverInterruptedDelegation(ctx, created.ID, config, time.Now().Add(2*time.Minute))
			}
			if err != nil || !recovered {
				t.Fatal(recovered, err)
			}
			status, err := db.Submission(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantReason := "queued", "lease_expired_no_task"
			if mode == "terminal" {
				wantState, wantReason = "succeeded", "terminal_history"
			}
			if mode == "model" {
				wantState, wantReason = "failed", "interrupted_model"
			}
			if mode == "delegation" {
				wantState, wantReason = "failed", "interrupted_delegation"
			}
			if status.State != wantState || status.LeaseExpiresAt != nil {
				t.Fatal(status)
			}
			receipts, err := db.RecoveryHistory(ctx, created.ID)
			if err != nil || len(receipts) != 1 || receipts[0].Reason != wantReason {
				t.Fatal(receipts, err)
			}
			if got := resumeSourceBytes(t, db, fence.TaskID); got != sourceBefore {
				t.Fatal("resume recovery mutated source history")
			}
			if root != "" {
				events, err := db.Read(ctx, root, 0, 100)
				if err != nil || len(events) == 0 || events[0].Kind != runtime.TaskStarted || events[0].Data.ParentTaskID != fence.TaskID || events[0].SessionID != fence.SessionID || events[0].Data.Privacy != fence.EffectivePrivacy {
					t.Fatal("resume root attribution changed", events, err)
				}
				if mode == "model" && (len(events) != 4 || events[len(events)-1].Data.Code != "interrupted_model") {
					t.Fatal("model recovery redispatched or repaired incorrectly", events)
				}
				if mode == "delegation" && (len(events) != 6 || events[len(events)-1].Data.Code != "interrupted_after_delegation") {
					t.Fatal("delegation recovery redispatched or repaired incorrectly", events)
				}
			}
		})
	}
}

func TestResumeRecoveryRejectsRemovedOrMutatedFence(t *testing.T) {
	for _, mode := range []string{"removed", "mutated"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, _ := submissionStore(t)
			created, _, fence, body, config := createClaimedResume(t, db, "fence-"+mode)
			sourceBefore := resumeSourceBytes(t, db, fence.TaskID)
			var envelope map[string]json.RawMessage
			if json.Unmarshal(body, &envelope) != nil {
				t.Fatal("invalid fixture")
			}
			if mode == "removed" {
				delete(envelope, "resume")
			} else {
				var resume map[string]any
				if json.Unmarshal(envelope["resume"], &resume) != nil {
					t.Fatal("invalid fence fixture")
				}
				resume["head_event_id"] = "different-head"
				envelope["resume"], _ = json.Marshal(resume)
			}
			changed, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.db.Exec(`UPDATE submissions SET request=? WHERE id=?`, changed, created.ID); err != nil {
				t.Fatal(err)
			}
			if ok, recoverErr := db.RecoverUndispatched(ctx, created.ID, config, time.Now().Add(2*time.Minute)); ok || !errors.Is(recoverErr, submissions.ErrInvalid) {
				t.Fatal("changed resume fence passed recovery", ok, recoverErr)
			}
			if got := resumeSourceBytes(t, db, fence.TaskID); got != sourceBefore {
				t.Fatal("failed recovery mutated source")
			}
		})
	}
}
