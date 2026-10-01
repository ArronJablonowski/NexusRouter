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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type branchSubmissionEnvelope struct {
	Version int                            `json:"version"`
	Request json.RawMessage                `json:"request"`
	Intent  submissionIntentProjection     `json:"intent"`
	Branch  *submissions.BranchSourceFence `json:"branch,omitempty"`
}

// submissionIntentProjection mirrors the durable, non-secret intent metadata
// in the application submission contract. Telemetry deliberately validates
// the contract boundary without re-running application classification.
type submissionIntentProjection struct {
	Version              int  `json:"version"`
	DomainExplicit       bool `json:"domain_explicit"`
	CapabilitiesExplicit bool `json:"capabilities_explicit"`
	Ambiguous            bool `json:"ambiguous"`
}

type branchRequestProjection struct {
	ContinueTaskID string `json:"ContinueTaskID"`
	LocalRequired  bool   `json:"LocalRequired"`
}

func submissionDeclaresBranch(body []byte) bool {
	return submissionDeclaresControl(body, "branch")
}

func submissionDeclaresControl(body []byte, control string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return false
	}
	for key := range object {
		if key == control || !canonicalBranchControlName(key, control) {
			return true
		}
	}
	return false
}

func parseBranchSubmission(body []byte) (submissions.BranchSourceFence, bool, error) {
	if len(body) < 1 || len(body) > submissions.MaxRequestBytes || !utf8.Valid(body) {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	var envelope branchSubmissionEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || (envelope.Version != 2 && envelope.Version != 3 && envelope.Version != 4 && envelope.Version != 5 && envelope.Version != 6) || envelope.Intent.Version != 1 || len(envelope.Request) == 0 {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, body) {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	if envelope.Branch == nil {
		return submissions.BranchSourceFence{}, false, nil
	}
	if envelope.Branch.Validate() != nil {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	if !uniqueBranchRequestJSON(envelope.Request) {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	var request branchRequestProjection
	if json.Unmarshal(envelope.Request, &request) != nil || request.ContinueTaskID != envelope.Branch.TaskID {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	wantPrivacy := "local_only"
	if envelope.Branch.SourcePrivacy == "cloud_allowed" && !request.LocalRequired {
		wantPrivacy = "cloud_allowed"
	}
	if envelope.Branch.EffectivePrivacy != wantPrivacy {
		return submissions.BranchSourceFence{}, false, submissions.ErrInvalid
	}
	return *envelope.Branch, true, nil
}

func uniqueBranchRequestJSON(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var read func(bool) bool
	read = func(requestRoot bool) bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if requestRoot && (!canonicalBranchControlName(key, "ContinueTaskID") || !canonicalBranchControlName(key, "LocalRequired")) {
					return false
				}
				if !read(false) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !read(false) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	return read(true) && func() bool { _, err := decoder.Token(); return err == io.EOF }()
}

func canonicalBranchControlName(key, control string) bool {
	normalize := func(value string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(value))
	}
	return normalize(key) != normalize(control) || key == control
}

// BranchSource derives a content-bound fence from one exact completed history.
// The result is not execution authority; creation and dispatch rederive it.
func (s *Store) BranchSource(ctx context.Context, task string) (submissions.BranchSourceFence, error) {
	if s == nil || ctx == nil || !sessions.ValidEventPageID(task) {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return submissions.BranchSourceFence{}, err
	}
	defer tx.Rollback()
	fence, err := deriveBranchSource(ctx, tx, task)
	if err != nil {
		return submissions.BranchSourceFence{}, err
	}
	return fence, tx.Commit()
}

func deriveBranchSource(ctx context.Context, tx *sql.Tx, task string) (submissions.BranchSourceFence, error) {
	var events []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, task, &events)
	if err != nil || snapshot.State != "completed" || len(events) == 0 {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	status := sessions.AssessContinuation(snapshot, events[max(0, len(events)-2):])
	if status.Validate() != nil || !status.HistoryEligible || status.State != "completed" || status.Reason != "completed" {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	start := events[0]
	var sourceRowID int64
	if tx.QueryRowContext(ctx, `SELECT rowid FROM task_heads WHERE task_id=? AND session_id=?`, task, snapshot.SessionID).Scan(&sourceRowID) != nil || sourceRowID < 1 {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	summary, relationalStart, relationalHead, err := readCanonicalSessionTask(ctx, tx, sourceRowID)
	if err != nil || summary.TaskID != task || summary.SessionID != snapshot.SessionID || summary.State != snapshot.State || summary.Sequence != snapshot.Sequence || relationalStart.ID != start.ID || relationalHead.ID != events[len(events)-1].ID || !sessionTaskParentExists(ctx, tx, start.Data.ParentTaskID, snapshot.SessionID, sourceRowID) || !sessionTaskRetryValid(ctx, tx, task, start.Data.RetryOfTaskID, sourceRowID) {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	if start.Kind != runtime.TaskStarted || start.WorkerID != "" || start.Data.DelegationOrigin != nil || branchParentIsWorker(ctx, tx, start.Data.ParentTaskID) {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("darwin-branch-history-v1\x00"))
	var size [8]byte
	for _, event := range events {
		if event.CorrelationID != task {
			return submissions.BranchSourceFence{}, submissions.ErrInvalid
		}
		body, encodeErr := event.Encode()
		if encodeErr != nil {
			return submissions.BranchSourceFence{}, submissions.ErrInvalid
		}
		var storedID string
		var raw []byte
		if tx.QueryRowContext(ctx, `SELECT id,body FROM events WHERE task_id=? AND sequence=?`, task, event.Sequence).Scan(&storedID, &raw) != nil || storedID != event.ID || !bytes.Equal(raw, body) {
			return submissions.BranchSourceFence{}, submissions.ErrInvalid
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(body)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(body)
	}
	privacy := start.Data.Privacy
	if privacy != "" && privacy != "local_only" && privacy != "cloud_allowed" {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	effective := "local_only"
	if privacy == "cloud_allowed" {
		effective = "cloud_allowed"
	}
	fence := submissions.BranchSourceFence{Version: 1, TaskID: task, SessionID: snapshot.SessionID, HeadSequence: snapshot.Sequence, HeadEventID: events[len(events)-1].ID, HistoryDigest: hex.EncodeToString(hash.Sum(nil)), SourcePrivacy: privacy, EffectivePrivacy: effective}
	if fence.Validate() != nil {
		return submissions.BranchSourceFence{}, submissions.ErrInvalid
	}
	return fence, nil
}

func branchParentIsWorker(ctx context.Context, tx *sql.Tx, parent string) bool {
	if parent == "" {
		return false
	}
	var raw []byte
	if tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=1`, parent).Scan(&raw) != nil {
		return true
	}
	var start runtime.Event
	if json.Unmarshal(raw, &start) != nil || start.Validate() != nil {
		return true
	}
	canonical, err := start.Encode()
	return err != nil || !bytes.Equal(canonical, raw) || start.Kind != runtime.TaskStarted || start.TaskID != parent || start.CorrelationID != parent || start.WorkerID != "" || start.Data.DelegationOrigin != nil
}

func sameBranchSource(a, b submissions.BranchSourceFence) bool { return a == b }

// CreateBranchSubmission atomically verifies the embedded completed-source
// fence and creates one ordinary queued submission without separate side state.
func (s *Store) CreateBranchSubmission(ctx context.Context, keyDigest, requestDigest, configDigest string, body []byte) (submissions.Status, error) {
	if !submissionDigest(keyDigest) || !submissionDigest(requestDigest) || !submissionDigest(configDigest) || len(body) < 1 || len(body) > submissions.MaxRequestBytes || !utf8.Valid(body) || !json.Valid(body) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	digest := sha256.Sum256(body)
	if requestDigest != hex.EncodeToString(digest[:]) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	embedded, branch, err := parseBranchSubmission(body)
	if err != nil || !branch {
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
		if err := tx.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, id).Scan(&stored); err != nil {
			return submissions.Status{}, err
		}
		storedFence, storedBranch, parseErr := parseBranchSubmission(stored)
		if parseErr != nil || !storedBranch || !sameBranchSource(storedFence, embedded) {
			return submissions.Status{}, submissions.ErrConflict
		}
		derived, deriveErr := deriveBranchSource(ctx, tx, storedFence.TaskID)
		if deriveErr != nil {
			return submissions.Status{}, submissions.ErrInvalid
		}
		derived.EffectivePrivacy = storedFence.EffectivePrivacy
		if !sameBranchSource(derived, storedFence) {
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
	derived, err := deriveBranchSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return submissions.Status{}, submissions.ErrInvalid
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameBranchSource(derived, embedded) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	var queued int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE state='queued'`).Scan(&queued); err != nil {
		return submissions.Status{}, err
	}
	if queued >= submissions.MaxQueued {
		return submissions.Status{}, submissions.ErrCapacity
	}
	id = rand.Text()
	now := submissionTime(time.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO submissions(id,key_digest,request_digest,config_digest,request,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, id, keyDigest, requestDigest, configDigest, body, now, now); err != nil {
		return submissions.Status{}, err
	}
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

// ValidateBranchSubmission rechecks a claimed branch before application work.
func (s *Store) ValidateBranchSubmission(ctx context.Context, id string) error {
	if s == nil || ctx == nil || !sessions.ValidEventPageID(id) {
		return submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateBranchSubmissionTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func validateBranchSubmissionTx(ctx context.Context, tx *sql.Tx, id string) error {
	var state, requestDigest string
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT state,request_digest,request FROM submissions WHERE id=?`, id).Scan(&state, &requestDigest, &body); err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	embedded, branch, err := parseBranchSubmission(body)
	if state != "running" || requestDigest != hex.EncodeToString(digest[:]) || err != nil || !branch {
		return submissions.ErrInvalid
	}
	derived, err := deriveBranchSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return submissions.ErrInvalid
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameBranchSource(derived, embedded) {
		return submissions.ErrInvalid
	}
	return nil
}

func validateBranchTaskStart(ctx context.Context, tx *sql.Tx, submissionID string, event runtime.Event) error {
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, submissionID).Scan(&body); err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if !submissionDeclaresBranch(body) {
		return nil
	}
	embedded, branch, err := parseBranchSubmission(body)
	if err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if !branch {
		return nil
	}
	derived, err := deriveBranchSource(ctx, tx, embedded.TaskID)
	if err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	derived.EffectivePrivacy = embedded.EffectivePrivacy
	if !sameBranchSource(derived, embedded) || event.TaskID == embedded.TaskID || event.SessionID != embedded.SessionID {
		return runtime.ErrExecutionLeaseLost
	}
	var starts int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=?`, submissionID).Scan(&starts); err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if starts == 0 && (event.Data.ParentTaskID != embedded.TaskID || event.Data.RetryOfTaskID != "" || !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy)) {
		return runtime.ErrExecutionLeaseLost
	}
	if event.Data.ParentTaskID == embedded.TaskID && !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy) {
		return runtime.ErrExecutionLeaseLost
	}
	if event.Data.Privacy != "" && !branchPrivacyAllows(embedded.EffectivePrivacy, event.Data.Privacy) {
		return runtime.ErrExecutionLeaseLost
	}
	internal, err := branchInternalTaskStart(ctx, tx, submissionID, embedded, event)
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
		if event.Data.RetryOfTaskID != "" {
			return runtime.ErrExecutionLeaseLost
		}
		return nil
	}
	if event.Data.RetryOfTaskID == "" || !latestBranchRootIsRetryPredecessor(ctx, tx, submissionID, embedded, event.Data.RetryOfTaskID) || validateRetryChain(ctx, tx, event.TaskID, event.Data.RetryOfTaskID) != nil {
		return runtime.ErrExecutionLeaseLost
	}
	return nil
}

func branchPrivacyAllows(maximum, actual string) bool {
	return actual == "local_only" || maximum == "cloud_allowed" && actual == "cloud_allowed"
}

func branchInternalTaskStart(ctx context.Context, tx *sql.Tx, submissionID string, fence submissions.BranchSourceFence, event runtime.Event) (bool, error) {
	workerStart := event.WorkerID != "" || event.Data.DelegationOrigin != nil
	if workerStart && (event.WorkerID == "" || event.Data.DelegationOrigin == nil || event.Data.ParentTaskID == "") {
		return false, submissions.ErrInvalid
	}
	if event.Data.ParentTaskID == "" {
		return false, nil
	}
	parent, err := readCanonicalBranchStart(ctx, tx, event.Data.ParentTaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if workerStart {
		if parent.SessionID != event.SessionID || parent.Data.SubmissionID != submissionID {
			return false, submissions.ErrInvalid
		}
		return branchAncestorReachesSource(ctx, tx, submissionID, fence, parent, 0), nil
	}
	if parent.WorkerID == "" || parent.Data.DelegationOrigin == nil {
		return false, nil
	}
	if parent.SessionID != event.SessionID || parent.Data.SubmissionID != submissionID {
		return false, submissions.ErrInvalid
	}
	return branchAncestorReachesSource(ctx, tx, submissionID, fence, parent, 0), nil
}

func readCanonicalBranchStart(ctx context.Context, tx *sql.Tx, task string) (runtime.Event, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=1`, task).Scan(&raw); err != nil {
		return runtime.Event{}, err
	}
	var start runtime.Event
	if json.Unmarshal(raw, &start) != nil || start.Validate() != nil {
		return runtime.Event{}, submissions.ErrInvalid
	}
	canonical, err := start.Encode()
	if err != nil || !bytes.Equal(canonical, raw) || start.Kind != runtime.TaskStarted || start.TaskID != task || start.CorrelationID != task {
		return runtime.Event{}, submissions.ErrInvalid
	}
	return start, nil
}

func branchAncestorReachesSource(ctx context.Context, tx *sql.Tx, submissionID string, fence submissions.BranchSourceFence, start runtime.Event, depth int) bool {
	if depth > 2 || start.SessionID != fence.SessionID || start.Data.SubmissionID != submissionID {
		return false
	}
	if start.Data.ParentTaskID == fence.TaskID {
		return start.WorkerID == "" && start.Data.DelegationOrigin == nil && branchPrivacyAllows(fence.EffectivePrivacy, start.Data.Privacy)
	}
	if start.Data.ParentTaskID == "" || start.WorkerID == "" || start.Data.DelegationOrigin == nil {
		return false
	}
	parent, err := readCanonicalBranchStart(ctx, tx, start.Data.ParentTaskID)
	return err == nil && branchAncestorReachesSource(ctx, tx, submissionID, fence, parent, depth+1)
}

func latestBranchRootIsRetryPredecessor(ctx context.Context, tx *sql.Tx, submissionID string, fence submissions.BranchSourceFence, predecessor string) bool {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT e.body FROM events e JOIN task_heads h ON h.task_id=e.task_id
		WHERE e.sequence=1 AND json_extract(e.body,'$.kind')='task.started'
		AND json_extract(e.body,'$.data.submission_id')=?
		AND json_extract(e.body,'$.data.parent_task_id')=?
		AND COALESCE(json_extract(e.body,'$.worker_id'),'')=''
		AND json_type(e.body,'$.data.delegation_origin') IS NULL
		ORDER BY h.rowid DESC LIMIT 1`, submissionID, fence.TaskID).Scan(&raw)
	if err != nil {
		return false
	}
	var start runtime.Event
	if json.Unmarshal(raw, &start) != nil || start.Validate() != nil {
		return false
	}
	canonical, err := start.Encode()
	return err == nil && bytes.Equal(canonical, raw) && start.Kind == runtime.TaskStarted && start.TaskID == predecessor && start.SessionID == fence.SessionID && start.CorrelationID == predecessor && start.Data.SubmissionID == submissionID && start.Data.ParentTaskID == fence.TaskID && branchPrivacyAllows(fence.EffectivePrivacy, start.Data.Privacy)
}
