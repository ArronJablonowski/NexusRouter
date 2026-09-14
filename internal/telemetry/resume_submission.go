package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

type resumeSubmissionEnvelope struct {
	Version int                            `json:"version"`
	Request json.RawMessage                `json:"request"`
	Intent  submissionIntentProjection     `json:"intent"`
	Resume  *submissions.ResumeSourceFence `json:"resume,omitempty"`
}

type resumeRequestProjection struct {
	SummaryAttemptID string
	Compaction       json.RawMessage
	Validation       string
	ModelID          string
	Prompt           string
	ContinueTaskID   string
	Messages         []providers.Message
	Domain           string
	Profile          string
	Capabilities     []string
	ContextTokens    int
	MaxCost          float64
	LocalRequired    bool
}

func submissionDeclaresResume(body []byte) bool {
	return submissionDeclaresControl(body, "resume")
}

func parseResumeSubmission(body []byte) (submissions.ResumeSourceFence, bool, error) {
	if len(body) < 1 || len(body) > submissions.MaxRequestBytes || !utf8.Valid(body) {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	var envelope resumeSubmissionEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 2 || envelope.Intent.Version != 1 || len(envelope.Request) == 0 {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, body) {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	if envelope.Resume == nil {
		return submissions.ResumeSourceFence{}, false, nil
	}
	if envelope.Resume.Validate() != nil || !uniqueBranchRequestJSON(envelope.Request) {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	var request resumeRequestProjection
	requestDecoder := json.NewDecoder(bytes.NewReader(envelope.Request))
	requestDecoder.DisallowUnknownFields()
	if requestDecoder.Decode(&request) != nil || requestDecoder.Decode(new(any)) != io.EOF {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	canonicalRequest, marshalErr := json.Marshal(request)
	if marshalErr != nil || !bytes.Equal(canonicalRequest, envelope.Request) || request.ContinueTaskID != envelope.Resume.TaskID || strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 1<<20 || request.SummaryAttemptID != "" || !bytes.Equal(request.Compaction, []byte("null")) || len(request.Messages) != 0 || request.ContextTokens < 0 || request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) || len(request.Domain) > 128 || len(request.Profile) > 128 || len(request.Capabilities) > 128 {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	seen := map[string]bool{}
	for _, capability := range request.Capabilities {
		if strings.TrimSpace(capability) == "" || len(capability) > 128 || seen[capability] {
			return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
		}
		seen[capability] = true
	}
	wantPrivacy := "local_only"
	if envelope.Resume.SourcePrivacy == "cloud_allowed" && !request.LocalRequired {
		wantPrivacy = "cloud_allowed"
	}
	if envelope.Resume.EffectivePrivacy != wantPrivacy {
		return submissions.ResumeSourceFence{}, false, submissions.ErrInvalid
	}
	return *envelope.Resume, true, nil
}

// ResumeSource derives exact authority from a safely recovered failed history.
func (s *Store) ResumeSource(ctx context.Context, task string) (submissions.ResumeSourceFence, error) {
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return submissions.ResumeSourceFence{}, err
	}
	defer tx.Rollback()
	fence, err := deriveResumeSource(ctx, tx, task)
	if err != nil {
		return submissions.ResumeSourceFence{}, err
	}
	return fence, tx.Commit()
}

func deriveResumeSource(ctx context.Context, tx *sql.Tx, task string) (submissions.ResumeSourceFence, error) {
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil || snapshot.State != "failed" || len(events) < 2 {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	reason := ""
	tail := events[max(0, len(events)-2):]
	status := sessions.AssessContinuation(snapshot, tail)
	if status.Validate() == nil && status.HistoryEligible && status.Reason == "recovered_delegation" {
		reason = status.Reason
	}
	terminal := events[len(events)-1]
	if reason == "" && terminal.Kind == runtime.TaskFailed && terminal.Data.Code == "interrupted_model" {
		plan, planErr := sessions.PlanInterruptedModel([][]runtime.Event{events[:len(events)-1]}, terminal.Time, false)
		if planErr == nil && len(plan.Events) == 1 && reflect.DeepEqual(plan.Events[0], terminal) {
			reason = "recovered_model"
		}
	}
	if reason == "" {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	start := events[0]
	if !sessions.ValidEventPageID(start.Data.SubmissionID) || !validResumeRecoveryAuthority(ctx, tx, start.Data.SubmissionID, task, reason, terminal.Time) {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	var sourceRowID int64
	if tx.QueryRowContext(ctx, `SELECT rowid FROM task_heads WHERE task_id=? AND session_id=?`, task, snapshot.SessionID).Scan(&sourceRowID) != nil || sourceRowID < 1 {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	summary, relationalStart, relationalHead, err := readCanonicalSessionTask(ctx, tx, sourceRowID)
	if err != nil || summary.TaskID != task || summary.SessionID != snapshot.SessionID || summary.State != snapshot.State || summary.Sequence != snapshot.Sequence || relationalStart.ID != start.ID || relationalHead.ID != events[len(events)-1].ID || !sessionTaskParentExists(ctx, tx, start.Data.ParentTaskID, snapshot.SessionID, sourceRowID) || !sessionTaskRetryValid(ctx, tx, task, start.Data.RetryOfTaskID, sourceRowID) {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	if start.Kind != runtime.TaskStarted || start.WorkerID != "" || start.Data.DelegationOrigin != nil || branchParentIsWorker(ctx, tx, start.Data.ParentTaskID) {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("darwin-resume-history-v1\x00"))
	var size [8]byte
	for _, event := range events {
		if event.CorrelationID != task {
			return submissions.ResumeSourceFence{}, submissions.ErrInvalid
		}
		body, encodeErr := event.Encode()
		if encodeErr != nil {
			return submissions.ResumeSourceFence{}, submissions.ErrInvalid
		}
		var storedID string
		var raw []byte
		if tx.QueryRowContext(ctx, `SELECT id,body FROM events WHERE task_id=? AND sequence=?`, task, event.Sequence).Scan(&storedID, &raw) != nil || storedID != event.ID || !bytes.Equal(raw, body) {
			return submissions.ResumeSourceFence{}, submissions.ErrInvalid
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(body)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(body)
	}
	privacy := start.Data.Privacy
	if privacy != "" && privacy != "local_only" && privacy != "cloud_allowed" {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	effective := "local_only"
	if privacy == "cloud_allowed" {
		effective = "cloud_allowed"
	}
	fence := submissions.ResumeSourceFence{Version: 1, TaskID: task, SessionID: snapshot.SessionID, HeadSequence: snapshot.Sequence, HeadEventID: terminal.ID, HistoryDigest: hex.EncodeToString(hash.Sum(nil)), SourceState: snapshot.State, RecoveryReason: reason, SourcePrivacy: privacy, EffectivePrivacy: effective}
	if fence.Validate() != nil {
		return submissions.ResumeSourceFence{}, submissions.ErrInvalid
	}
	return fence, nil
}

func validResumeRecoveryAuthority(ctx context.Context, tx *sql.Tx, submissionID, task, reason string, terminalTime time.Time) bool {
	status, err := readSubmission(ctx, tx, submissionID)
	if err != nil || status.State != "failed" || status.ErrorCode != "execution_failed" || status.CancelRequested || status.LeaseExpiresAt != nil || status.Result == nil || status.Result.TaskID != task || status.Result.Text != "" {
		return false
	}
	expectedResult, err := json.Marshal(submissions.Result{TaskID: task, AuditStatus: "not_recovered", PreviousTaskIDs: []string{}})
	if err != nil {
		return false
	}
	var rawResult []byte
	if tx.QueryRowContext(ctx, `SELECT result FROM submissions WHERE id=?`, submissionID).Scan(&rawResult) != nil || !bytes.Equal(rawResult, expectedResult) {
		return false
	}
	want := "interrupted_model"
	if reason == "recovered_delegation" {
		want = "interrupted_delegation"
	}
	rows, err := tx.QueryContext(ctx, `SELECT
CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END,
CASE WHEN length(CAST(prior_token_digest AS BLOB))=64 THEN prior_token_digest END,
CASE WHEN length(body)<=4096 THEN body END
FROM submission_recoveries WHERE submission_id=? ORDER BY rowid LIMIT 5`, submissionID)
	if err != nil {
		return false
	}
	defer rows.Close()
	count, terminal, terminalAt := 0, 0, 0
	for rows.Next() {
		count++
		var recordID, priorDigest sql.NullString
		var raw []byte
		var receipt submissions.Recovery
		if count > 4 || rows.Scan(&recordID, &priorDigest, &raw) != nil || !recordID.Valid || !priorDigest.Valid || !submissionDigest(priorDigest.String) || json.Unmarshal(raw, &receipt) != nil || receipt.Validate() != nil || receipt.ID != recordID.String || receipt.SubmissionID != submissionID {
			return false
		}
		canonical, marshalErr := json.Marshal(receipt)
		if marshalErr != nil || !bytes.Equal(canonical, raw) {
			return false
		}
		if receipt.Reason == "interrupted_model" || receipt.Reason == "interrupted_delegation" || receipt.Reason == "terminal_history" {
			terminal++
			terminalAt = count
			if terminal != 1 || receipt.Reason != want || receipt.Action != "failed" || !receipt.Time.Equal(terminalTime) {
				return false
			}
		} else if receipt.Action != "queued" || receipt.Reason != "lease_expired_no_task" || !receipt.Time.Before(terminalTime) {
			return false
		}
	}
	return rows.Err() == nil && count > 0 && terminal == 1 && terminalAt == count
}

func sameResumeSource(a, b submissions.ResumeSourceFence) bool { return a == b }

func (s *Store) CreateResumeSubmission(ctx context.Context, keyDigest, requestDigest, configDigest string, body []byte) (submissions.Status, error) {
	if !submissionDigest(keyDigest) || !submissionDigest(requestDigest) || !submissionDigest(configDigest) || len(body) < 1 || len(body) > submissions.MaxRequestBytes || !utf8.Valid(body) || !json.Valid(body) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	digest := sha256.Sum256(body)
	if requestDigest != hex.EncodeToString(digest[:]) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	embedded, resume, err := parseResumeSubmission(body)
	if err != nil || !resume {
		return submissions.Status{}, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return submissions.Status{}, err
	}
	defer tx.Rollback()
	id, previousRequest, previousConfig, err := readSubmissionKeyRecord(ctx, tx, keyDigest)
	if err == nil {
		if requestDigest != previousRequest || configDigest != previousConfig {
			return submissions.Status{}, submissions.ErrConflict
		}
		var stored []byte
		if err = tx.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, id).Scan(&stored); err != nil {
			return submissions.Status{}, err
		}
		storedFence, storedResume, parseErr := parseResumeSubmission(stored)
		if parseErr != nil || !storedResume || !sameResumeSource(storedFence, embedded) {
			return submissions.Status{}, submissions.ErrConflict
		}
		derived, deriveErr := deriveResumeSource(ctx, tx, storedFence.TaskID)
		if deriveErr != nil {
			return submissions.Status{}, submissions.ErrInvalid
		}
		derived.EffectivePrivacy = storedFence.EffectivePrivacy
		if !sameResumeSource(derived, storedFence) {
			return submissions.Status{}, submissions.ErrInvalid
		}
		status, readErr := readSubmission(ctx, tx, id)
		if readErr != nil {
			return status, readErr
		}
		return status, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return submissions.Status{}, err
	}
	derived, err := deriveResumeSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return submissions.Status{}, submissions.ErrInvalid
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameResumeSource(derived, embedded) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	var queued int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE state='queued'`).Scan(&queued); err != nil {
		return submissions.Status{}, err
	}
	if queued >= submissions.MaxQueued {
		return submissions.Status{}, submissions.ErrCapacity
	}
	id = rand.Text()
	now := submissionTime(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(id,key_digest,request_digest,config_digest,request,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, id, keyDigest, requestDigest, configDigest, body, now, now); err != nil {
		return submissions.Status{}, err
	}
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

func (s *Store) ValidateResumeSubmission(ctx context.Context, id string) error {
	if s == nil || ctx == nil || !sessions.ValidEventPageID(id) {
		return submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateResumeSubmissionTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func validateResumeSubmissionTx(ctx context.Context, tx *sql.Tx, id string) error {
	var state, requestDigest string
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT state,request_digest,request FROM submissions WHERE id=?`, id).Scan(&state, &requestDigest, &body); err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	embedded, resume, err := parseResumeSubmission(body)
	if state != "running" || requestDigest != hex.EncodeToString(digest[:]) || err != nil || !resume {
		return submissions.ErrInvalid
	}
	derived, err := deriveResumeSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return submissions.ErrInvalid
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameResumeSource(derived, embedded) {
		return submissions.ErrInvalid
	}
	return nil
}

func resumeAsBranch(f submissions.ResumeSourceFence) submissions.BranchSourceFence {
	return submissions.BranchSourceFence{Version: 1, TaskID: f.TaskID, SessionID: f.SessionID, HeadSequence: f.HeadSequence, HeadEventID: f.HeadEventID, HistoryDigest: f.HistoryDigest, SourcePrivacy: f.SourcePrivacy, EffectivePrivacy: f.EffectivePrivacy}
}

func validateResumeTaskStart(ctx context.Context, tx *sql.Tx, submissionID string, event runtime.Event) error {
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, submissionID).Scan(&body); err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if !submissionDeclaresResume(body) {
		return nil
	}
	embedded, resume, err := parseResumeSubmission(body)
	if err != nil || !resume {
		return runtime.ErrExecutionLeaseLost
	}
	derived, err := deriveResumeSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameResumeSource(derived, embedded) || event.TaskID == embedded.TaskID || event.SessionID != embedded.SessionID {
		return runtime.ErrExecutionLeaseLost
	}
	base := resumeAsBranch(embedded)
	var starts int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=?`, submissionID).Scan(&starts); err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if starts == 0 && (event.Data.ParentTaskID != embedded.TaskID || event.Data.RetryOfTaskID != "" || !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy)) {
		return runtime.ErrExecutionLeaseLost
	}
	if event.Data.Privacy != "" && !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy) {
		return runtime.ErrExecutionLeaseLost
	}
	internal, err := branchInternalTaskStart(ctx, tx, submissionID, base, event)
	if err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if internal {
		return nil
	}
	if event.Data.ParentTaskID != embedded.TaskID || !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy) {
		return runtime.ErrExecutionLeaseLost
	}
	if starts == 0 {
		return nil
	}
	if event.Data.RetryOfTaskID == "" || !latestBranchRootIsRetryPredecessor(ctx, tx, submissionID, base, event.Data.RetryOfTaskID) || validateRetryChain(ctx, tx, event.TaskID, event.Data.RetryOfTaskID) != nil {
		return runtime.ErrExecutionLeaseLost
	}
	return nil
}
