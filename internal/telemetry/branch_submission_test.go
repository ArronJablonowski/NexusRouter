package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func branchEnvelope(t *testing.T, fence submissions.BranchSourceFence, local bool) ([]byte, string) {
	t.Helper()
	request := struct {
		ContinueTaskID string `json:"ContinueTaskID"`
		LocalRequired  bool   `json:"LocalRequired"`
	}{fence.TaskID, local}
	body, err := json.Marshal(struct {
		Version int                            `json:"version"`
		Request any                            `json:"request"`
		Intent  submissionIntentProjection     `json:"intent"`
		Branch  *submissions.BranchSourceFence `json:"branch,omitempty"`
	}{2, request, submissionIntentProjection{Version: 1}, &fence})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:])
}

func completedBranchSource(t *testing.T, db *Store, task, privacy string) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	start := runtime.Event{Version: 1, ID: task + "-start", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Privacy: privacy}}
	end := runtime.Event{Version: 1, ID: task + "-end", TaskID: task, SessionID: task + "-session", CorrelationID: task, Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskCompleted, Data: runtime.Data{Text: "done"}}
	if err := db.Append(context.Background(), 0, start); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(context.Background(), 1, end); err != nil {
		t.Fatal(err)
	}
}

func TestBranchSubmissionAtomicCreationIdempotencyAndStartFence(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "branch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	completedBranchSource(t, db, "source", "cloud_allowed")
	fence, err := db.BranchSource(ctx, "source")
	if err != nil || fence.Validate() != nil || fence.EffectivePrivacy != "cloud_allowed" {
		t.Fatal(fence, err)
	}
	body, requestDigest := branchEnvelope(t, fence, false)
	key, config := submitDigest("branch-key"), submitDigest("config")
	created, err := db.CreateBranchSubmission(ctx, key, requestDigest, config, body)
	if err != nil || created.State != "queued" {
		t.Fatal(created, err)
	}
	again, err := db.CreateBranchSubmission(ctx, key, requestDigest, config, body)
	if err != nil || again.ID != created.ID {
		t.Fatal(again, err)
	}
	if _, err := db.CreateSubmission(ctx, key, requestDigest, config, body); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("generic submission admitted branch envelope", err)
	}
	localFence := fence
	localFence.EffectivePrivacy = "local_only"
	changed, changedDigest := branchEnvelope(t, localFence, true)
	if _, err := db.CreateBranchSubmission(ctx, key, changedDigest, config, changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("changed idempotent branch did not conflict", err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != created.ID || db.ValidateBranchSubmission(ctx, created.ID) != nil {
		t.Fatal(claim.Status, err)
	}
	child := runtime.Event{Version: 1, ID: "child-start", TaskID: "child", SessionID: fence.SessionID, CorrelationID: "child", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy}}
	if err := db.AppendSubmission(ctx, 0, child, created.ID, claim.Token); err != nil {
		t.Fatal("exact branch start rejected", err)
	}
}

func TestBranchSubmissionRejectsSourceDriftAtIdempotencyClaimAndStart(t *testing.T) {
	for _, boundary := range []string{"idempotency", "claim", "start"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, filepath.Join(t.TempDir(), "drift.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			completedBranchSource(t, db, "source", "local_only")
			fence, _ := db.BranchSource(ctx, "source")
			body, requestDigest := branchEnvelope(t, fence, true)
			config := submitDigest("config")
			created, err := db.CreateBranchSubmission(ctx, submitDigest("key"), requestDigest, config, body)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "idempotency" {
				if _, err := db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='source' AND sequence=2`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.CreateBranchSubmission(ctx, submitDigest("key"), requestDigest, config, body); !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal("source drift passed idempotent branch recheck", err)
				}
				return
			}
			claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "start" && db.ValidateBranchSubmission(ctx, created.ID) != nil {
				t.Fatal("valid claim-time fence rejected")
			}
			if _, err := db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='source' AND sequence=2`); err != nil {
				t.Fatal(err)
			}
			if boundary == "claim" {
				if err := db.ValidateBranchSubmission(ctx, created.ID); !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal("drift passed claim-time fence", err)
				}
				return
			}
			child := runtime.Event{Version: 1, ID: "child-start", TaskID: "child", SessionID: fence.SessionID, CorrelationID: "child", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy}}
			if err := db.AppendSubmission(ctx, 0, child, created.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
				t.Fatal("drift passed start transaction fence", err)
			}
			var count int
			if queryErr := db.db.QueryRow(`SELECT count(*) FROM task_heads WHERE task_id='child'`).Scan(&count); queryErr != nil || count != 0 {
				t.Fatal("failed branch start left residue", count, queryErr)
			}
		})
	}
}

func TestBranchSubmissionRejectsUnsafeSourcesAndAmbiguousControls(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "unsafe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(100, 0).UTC()
	worker := runtime.Event{Version: 1, ID: "worker-start", TaskID: "worker", SessionID: "session", CorrelationID: "worker", WorkerID: "owner", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: "root", DelegationOrigin: &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}}}
	if err := db.Append(ctx, 0, worker); err != nil {
		t.Fatal(err)
	}
	end := runtime.Event{Version: 1, ID: "worker-end", TaskID: "worker", SessionID: "session", CorrelationID: "worker", WorkerID: "owner", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskCompleted}
	if err := db.Append(ctx, 1, end); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BranchSource(ctx, "worker"); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("worker source admitted", err)
	}
	for _, malformed := range []struct {
		task   string
		parent string
		retry  string
	}{
		{task: "forged-parent-source", parent: "missing-parent"},
		{task: "forged-retry-source", retry: "missing-predecessor"},
	} {
		start := runtime.Event{Version: 1, ID: malformed.task + "-start", TaskID: malformed.task, SessionID: "forged-session", CorrelationID: malformed.task, Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: malformed.parent, RetryOfTaskID: malformed.retry, Privacy: "local_only"}}
		end := runtime.Event{Version: 1, ID: malformed.task + "-end", TaskID: malformed.task, SessionID: "forged-session", CorrelationID: malformed.task, Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskCompleted}
		if err := db.Append(ctx, 0, start); err != nil {
			t.Fatal(err)
		}
		if err := db.Append(ctx, 1, end); err != nil {
			t.Fatal(err)
		}
		if _, err := db.BranchSource(ctx, malformed.task); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatalf("forged source lineage admitted for %s: %v", malformed.task, err)
		}
	}
	completedBranchSource(t, db, "source", "cloud_allowed")
	fence, _ := db.BranchSource(ctx, "source")
	body, _ := branchEnvelope(t, fence, false)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		t.Fatal("invalid fixture")
	}
	request := `{"ContinueTaskID":"wrong","ContinueTaskID":"source","LocalRequired":false}`
	body = []byte(`{"version":2,"request":` + request + `,"intent":{"version":1,"domain_explicit":false,"capabilities_explicit":false,"ambiguous":false},"branch":` + string(envelope["branch"]) + `}`)
	digest := sha256.Sum256(body)
	if _, err := db.CreateBranchSubmission(ctx, submitDigest("ambiguous"), hex.EncodeToString(digest[:]), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
		t.Fatal("duplicate branch controls admitted", err)
	}
	for index, request := range []string{
		`{"continue_task_id":"source","LocalRequired":false}`,
		`{"ContinueTaskID":"source","local_required":false}`,
		`{"continue-task-id":"source","LocalRequired":false}`,
	} {
		body = []byte(`{"version":2,"request":` + request + `,"intent":{"version":1,"domain_explicit":false,"capabilities_explicit":false,"ambiguous":false},"branch":` + string(envelope["branch"]) + `}`)
		digest = sha256.Sum256(body)
		if _, err := db.CreateBranchSubmission(ctx, submitDigest(request), hex.EncodeToString(digest[:]), submitDigest("config"), body); !errors.Is(err, submissions.ErrInvalid) {
			t.Fatalf("noncanonical branch control %d admitted: %v", index, err)
		}
	}
}

func TestBranchSubmissionRequiresCanonicalV2IntentMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "intent-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	completedBranchSource(t, db, "source", "local_only")
	fence, err := db.BranchSource(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := branchEnvelope(t, fence, true)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		t.Fatal("invalid fixture")
	}
	for name, raw := range map[string][]byte{
		"legacy version":       []byte(`{"version":1,"request":` + string(envelope["request"]) + `,"branch":` + string(envelope["branch"]) + `}`),
		"missing intent":       []byte(`{"version":2,"request":` + string(envelope["request"]) + `,"branch":` + string(envelope["branch"]) + `}`),
		"bad intent version":   []byte(`{"version":2,"request":` + string(envelope["request"]) + `,"intent":{"version":2,"domain_explicit":false,"capabilities_explicit":false,"ambiguous":false},"branch":` + string(envelope["branch"]) + `}`),
		"unknown intent field": []byte(`{"version":2,"request":` + string(envelope["request"]) + `,"intent":{"version":1,"domain_explicit":false,"capabilities_explicit":false,"ambiguous":false,"extra":true},"branch":` + string(envelope["branch"]) + `}`),
	} {
		t.Run(name, func(t *testing.T) {
			digest := sha256.Sum256(raw)
			if _, err := db.CreateBranchSubmission(ctx, submitDigest(name), hex.EncodeToString(digest[:]), submitDigest("config"), raw); !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal("invalid submission contract admitted", err)
			}
		})
	}
}

func TestBranchStartPrivacyMaximumAndWorkerLineage(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "start-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	completedBranchSource(t, db, "source", "cloud_allowed")
	fence, _ := db.BranchSource(ctx, "source")
	body, requestDigest := branchEnvelope(t, fence, false)
	config := submitDigest("config")
	created, err := db.CreateBranchSubmission(ctx, submitDigest("policy-key"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	root := runtime.Event{Version: 1, ID: "root-start", TaskID: "root", SessionID: fence.SessionID, CorrelationID: "root", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, Privacy: "local_only", ProviderID: "local", ModelID: "model"}}
	if err := db.AppendSubmission(ctx, 0, root, created.ID, claim.Token); err != nil {
		t.Fatal("cloud-allowed maximum rejected stricter local task", err)
	}
	turn := runtime.Event{Version: 1, ID: "root-turn", TaskID: "root", SessionID: fence.SessionID, CorrelationID: "root", Sequence: 2, Time: time.Now().UTC(), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "local", ModelID: "model"}}
	failed := runtime.Event{Version: 1, ID: "root-failed", TaskID: "root", SessionID: fence.SessionID, CorrelationID: "root", Sequence: 3, Time: time.Now().UTC(), Kind: runtime.TaskFailed, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Code: "provider_retryable_no_output"}}
	if err := db.AppendSubmission(ctx, 1, turn, created.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendSubmission(ctx, 2, failed, created.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	fallback := runtime.Event{Version: 1, ID: "fallback-start", TaskID: "fallback", SessionID: fence.SessionID, CorrelationID: "fallback", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: fence.TaskID, RetryOfTaskID: "root", Privacy: "local_only", ProviderID: "local", ModelID: "fallback"}}
	if err := db.AppendSubmission(ctx, 0, fallback, created.ID, claim.Token); err != nil {
		t.Fatal("local fallback under cloud-allowed maximum rejected", err)
	}
	foreign := runtime.Event{Version: 1, ID: "foreign-start", TaskID: "foreign", SessionID: fence.SessionID, CorrelationID: "foreign", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, foreign); err != nil {
		t.Fatal(err)
	}
	origin := &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	forged := runtime.Event{Version: 1, ID: "forged-start", TaskID: "forged", SessionID: fence.SessionID, CorrelationID: "forged", WorkerID: "worker", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: created.ID, ParentTaskID: "foreign", DelegationOrigin: origin}}
	if err := db.AppendSubmission(ctx, 0, forged, created.ID, claim.Token); !errors.Is(err, runtime.ErrExecutionLeaseLost) {
		t.Fatal("foreign worker lineage bypassed branch root fence", err)
	}
}

func appendCompletedBranchTask(t *testing.T, db *Store, claim submissions.Claim, fence submissions.BranchSourceFence) string {
	t.Helper()
	task := "branch-child"
	now := time.Now().UTC().Add(-time.Second)
	kinds := []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.EvaluationRecorded, runtime.TaskCompleted}
	for i, kind := range kinds {
		e := runtime.Event{Version: 1, ID: task + "-event-" + string(rune('1'+i)), TaskID: task, SessionID: fence.SessionID, CorrelationID: task, Sequence: int64(i + 1), Time: now.Add(time.Duration(i) * time.Millisecond), Kind: kind}
		e.TurnID, e.AttemptID = "turn", "attempt"
		e.Data.ModelID, e.Data.ProviderID = "model", "fixture"
		switch kind {
		case runtime.TaskStarted:
			e.TurnID, e.AttemptID = "", ""
			e.Data = runtime.Data{SubmissionID: claim.Status.ID, ParentTaskID: fence.TaskID, Privacy: fence.EffectivePrivacy, ModelID: "model", ProviderID: "fixture", Messages: []providers.Message{{Role: "user", Content: "branch question"}}}
		case runtime.TurnStarted:
		case runtime.TurnCompleted:
			e.Data.Text, e.Data.FinishReason = "answer", "stop"
		case runtime.EvaluationRecorded:
			accepted := true
			e.Data.Accepted, e.Data.Code = &accepted, "deterministic.nonempty_text.v1"
		case runtime.TaskCompleted:
			e.Data.Text = "answer"
		}
		if err := db.AppendSubmission(context.Background(), int64(i), e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func TestBranchSubmissionRecoveryAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "branch-recovery.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	completedBranchSource(t, db, "source", "local_only")
	fence, err := db.BranchSource(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	body, requestDigest := branchEnvelope(t, fence, true)
	config := submitDigest("config")
	queued, err := db.CreateBranchSubmission(ctx, submitDigest("branch-recovery-key"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil || claim.Status.ID != queued.ID {
		t.Fatal(claim.Status, err)
	}
	task := appendCompletedBranchTask(t, db, claim, fence)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if ok, recoverErr := db.RecoverTerminalSubmission(ctx, queued.ID, config, time.Now().Add(2*time.Minute)); recoverErr != nil || !ok {
		t.Fatal(ok, recoverErr)
	}
	status, err := db.Submission(ctx, queued.ID)
	if err != nil || status.State != "succeeded" || status.Result == nil || status.Result.TaskID != task || status.Result.Text != "answer" {
		t.Fatal(status, err)
	}
}

func TestBranchSubmissionUndispatchedRecoveryRevalidatesOnReclaim(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "branch-undispatched.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	completedBranchSource(t, db, "source", "local_only")
	fence, _ := db.BranchSource(ctx, "source")
	body, requestDigest := branchEnvelope(t, fence, true)
	config := submitDigest("config")
	queued, err := db.CreateBranchSubmission(ctx, submitDigest("branch-undispatched-key"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if ok, recoverErr := db.RecoverUndispatched(ctx, queued.ID, config, time.Now().Add(2*time.Minute)); recoverErr != nil || !ok {
		t.Fatal(ok, recoverErr)
	}
	second, err := db.ClaimSubmission(ctx, config, time.Now().Add(3*time.Minute), time.Minute)
	if err != nil || second.Status.ID != queued.ID || db.ValidateBranchSubmission(ctx, queued.ID) != nil {
		t.Fatal(second.Status, err)
	}
	if _, err = db.RenewSubmission(ctx, queued.ID, first.Token, time.Now().Add(3*time.Minute), time.Minute); !errors.Is(err, submissions.ErrLeaseLost) {
		t.Fatal("expired owner retained authority", err)
	}
}

func TestBranchSubmissionUndispatchedRecoveryRejectsSourceCorruption(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	completedBranchSource(t, db, "source", "local_only")
	fence, _ := db.BranchSource(ctx, "source")
	body, requestDigest := branchEnvelope(t, fence, true)
	config := submitDigest("config")
	queued, err := db.CreateBranchSubmission(ctx, submitDigest("branch-undispatched-corrupt"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='source' AND sequence=2`); err != nil {
		t.Fatal(err)
	}
	if ok, recoverErr := db.RecoverUndispatched(ctx, queued.ID, config, time.Now().Add(2*time.Minute)); ok || !errors.Is(recoverErr, submissions.ErrInvalid) {
		t.Fatal("corrupt branch was requeued", ok, recoverErr)
	}
	status, err := db.Submission(ctx, queued.ID)
	if err != nil || status.State != "running" || status.Result != nil {
		t.Fatal(status, err)
	}
}

func TestBranchTerminalRecoveryFailsClosedAfterSourceCorruption(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	completedBranchSource(t, db, "source", "local_only")
	fence, _ := db.BranchSource(ctx, "source")
	body, requestDigest := branchEnvelope(t, fence, true)
	config := submitDigest("config")
	queued, err := db.CreateBranchSubmission(ctx, submitDigest("branch-corruption-key"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedBranchTask(t, db, claim, fence)
	if _, err = db.db.Exec(`UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='source' AND sequence=2`); err != nil {
		t.Fatal(err)
	}
	var corrupted []byte
	if err = db.db.QueryRow(`SELECT body FROM events WHERE task_id='source' AND sequence=2`).Scan(&corrupted); err != nil {
		t.Fatal(err)
	}
	if ok, recoverErr := db.RecoverTerminalSubmission(ctx, queued.ID, config, time.Now().Add(2*time.Minute)); ok || recoverErr == nil {
		t.Fatal("corrupt branch source passed terminal recovery", ok, recoverErr)
	}
	status, err := db.Submission(ctx, queued.ID)
	if err != nil || status.State != "running" || status.Result != nil {
		t.Fatal(status, err)
	}
	var after []byte
	if err = db.db.QueryRow(`SELECT body FROM events WHERE task_id='source' AND sequence=2`).Scan(&after); err != nil || !bytes.Equal(after, corrupted) {
		t.Fatal("failed recovery mutated source", err)
	}
}

func TestBranchRecoveryRejectsRemovedDurableFence(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	completedBranchSource(t, db, "source", "local_only")
	fence, _ := db.BranchSource(ctx, "source")
	body, requestDigest := branchEnvelope(t, fence, true)
	config := submitDigest("config")
	queued, err := db.CreateBranchSubmission(ctx, submitDigest("branch-removed-fence"), requestDigest, config, body)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, config, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedBranchTask(t, db, claim, fence)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		t.Fatal("invalid fixture")
	}
	delete(envelope, "branch")
	withoutFence, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`UPDATE submissions SET request=? WHERE id=?`, withoutFence, queued.ID); err != nil {
		t.Fatal(err)
	}
	if ok, recoverErr := db.RecoverTerminalSubmission(ctx, queued.ID, config, time.Now().Add(2*time.Minute)); ok || !errors.Is(recoverErr, submissions.ErrInvalid) {
		t.Fatal("removed branch fence passed recovery", ok, recoverErr)
	}
	status, err := db.Submission(ctx, queued.ID)
	if err != nil || status.State != "running" || status.Result != nil {
		t.Fatal(status, err)
	}
}
