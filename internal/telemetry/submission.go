package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func submissionDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func submissionTTL(now time.Time, ttl time.Duration) bool {
	return !now.IsZero() && ttl > 0 && ttl <= 5*time.Minute
}
func submissionTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func submissionTx(ctx context.Context, s *Store) (*sql.Tx, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE submissions SET id=id WHERE id='' "); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}

func readSubmission(ctx context.Context, tx *sql.Tx, id string) (submissions.Status, error) {
	o := submissions.Status{Version: 1, ID: id, TaskIDs: []string{}}
	var created, updated, expires string
	var result []byte
	var resultSize int64
	err := tx.QueryRowContext(ctx, "SELECT state,created_at,updated_at,config_digest,cancel_requested,lease_expires_at,COALESCE(length(result),0) FROM submissions WHERE id=?", id).Scan(&o.State, &created, &updated, &o.ConfigDigest, &o.CancelRequested, &expires, &resultSize)
	if err != nil {
		return o, err
	}
	if o.State != "queued" && o.State != "running" && o.State != "succeeded" && o.State != "failed" && o.State != "canceled" {
		return o, submissions.ErrInvalid
	}
	o.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return o, submissions.ErrInvalid
	}
	o.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return o, submissions.ErrInvalid
	}
	if expires != "" {
		at, e := time.Parse(time.RFC3339Nano, expires)
		if e != nil {
			return o, submissions.ErrInvalid
		}
		o.LeaseExpiresAt = &at
		o.LeaseExpired = o.State == "running" && !time.Now().Before(at)
	}
	if resultSize > submissions.MaxRequestBytes {
		return o, submissions.ErrInvalid
	}
	if err := tx.QueryRowContext(ctx, "SELECT result,error_code FROM submissions WHERE id=?", id).Scan(&result, &o.ErrorCode); err != nil {
		return o, err
	}
	if len(result) > 0 && json.Unmarshal(result, &o.Result) != nil {
		return o, submissions.ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT task_id FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=? ORDER BY rowid LIMIT 1001", id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var task string
		if err := rows.Scan(&task); err != nil {
			return o, err
		}
		o.TaskIDs = append(o.TaskIDs, task)
		if len(o.TaskIDs) > 1000 {
			return o, submissions.ErrInvalid
		}
	}
	return o, rows.Err()
}

func readSubmissionKeyRecord(ctx context.Context, tx *sql.Tx, keyDigest string) (id, requestDigest, configDigest string, err error) {
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT id,request_digest,config_digest,length(CAST(request AS BLOB)) FROM submissions WHERE key_digest=?`, keyDigest).Scan(&id, &requestDigest, &configDigest, &size)
	if err != nil {
		return "", "", "", err
	}
	if !sessions.ValidEventPageID(id) || !submissionDigest(requestDigest) || !submissionDigest(configDigest) || size < 1 || size > submissions.MaxRequestBytes {
		return "", "", "", submissions.ErrInvalid
	}
	var body []byte
	if err = tx.QueryRowContext(ctx, `SELECT request FROM submissions WHERE id=?`, id).Scan(&body); err != nil {
		return "", "", "", err
	}
	digest := sha256.Sum256(body)
	if int64(len(body)) != size || requestDigest != hex.EncodeToString(digest[:]) || !utf8.Valid(body) || !json.Valid(body) {
		return "", "", "", submissions.ErrInvalid
	}
	return id, requestDigest, configDigest, nil
}

func (s *Store) CreateSubmission(ctx context.Context, keyDigest, requestDigest, configDigest string, body []byte) (submissions.Status, error) {
	if !submissionDigest(keyDigest) || !submissionDigest(requestDigest) || !submissionDigest(configDigest) || len(body) < 1 || len(body) > submissions.MaxRequestBytes || !utf8.Valid(body) || !json.Valid(body) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	if submissionDeclaresBranch(body) || submissionDeclaresResume(body) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	digest := sha256.Sum256(body)
	if requestDigest != hex.EncodeToString(digest[:]) {
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
		status, e := readSubmission(ctx, tx, id)
		if e != nil {
			return status, e
		}
		return status, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return submissions.Status{}, err
	}
	var queued int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM submissions WHERE state='queued'").Scan(&queued); err != nil {
		return submissions.Status{}, err
	}
	if queued >= submissions.MaxQueued {
		return submissions.Status{}, submissions.ErrCapacity
	}
	id = rand.Text()
	now := submissionTime(time.Now())
	if _, err := tx.ExecContext(ctx, "INSERT INTO submissions(id,key_digest,request_digest,config_digest,request,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)", id, keyDigest, requestDigest, configDigest, body, now, now); err != nil {
		return submissions.Status{}, err
	}
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

func (s *Store) Submission(ctx context.Context, id string) (submissions.Status, error) {
	if !sessions.ValidEventPageID(id) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return submissions.Status{}, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return submissions.Status{}, err
	}
	if version < 12 {
		return submissions.Status{}, sql.ErrNoRows
	}
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

// SubmissionByKey returns an existing submission only when the idempotency
// key, canonical request, and active configuration all match. It never creates
// work, which makes it safe to use while authorizing stream resumption.
func (s *Store) SubmissionByKey(ctx context.Context, keyDigest, requestDigest, configDigest string) (submissions.Status, error) {
	if !submissionDigest(keyDigest) || !submissionDigest(requestDigest) || !submissionDigest(configDigest) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return submissions.Status{}, err
	}
	defer tx.Rollback()
	id, storedRequest, storedConfig, err := readSubmissionKeyRecord(ctx, tx, keyDigest)
	if err != nil {
		return submissions.Status{}, err
	}
	if storedRequest != requestDigest || storedConfig != configDigest {
		return submissions.Status{}, submissions.ErrConflict
	}
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

func (s *Store) ClaimSubmission(ctx context.Context, digest string, now time.Time, ttl time.Duration) (submissions.Claim, error) {
	var out submissions.Claim
	if !submissionDigest(digest) || !submissionTTL(now, ttl) {
		return out, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var id, created string
	var size int64
	if err := tx.QueryRowContext(ctx, "SELECT id,created_at,length(request) FROM submissions WHERE state='queued' AND config_digest=? ORDER BY rowid LIMIT 1", digest).Scan(&id, &created, &size); err != nil {
		return out, err
	}
	if size < 1 || size > submissions.MaxRequestBytes {
		return out, submissions.ErrInvalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return out, submissions.ErrInvalid
	}
	// The caller can capture now before waiting for the SQLite writer. Never
	// commit a claim whose updated time predates the queued record it claims.
	if now.Before(createdAt) {
		now = createdAt
	}
	if err := tx.QueryRowContext(ctx, "SELECT request FROM submissions WHERE id=?", id).Scan(&out.Request); err != nil {
		return out, err
	}
	out.Token = rand.Text()
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET state='running',token=?,lease_expires_at=?,updated_at=? WHERE id=?", out.Token, submissionTime(now.Add(ttl)), submissionTime(now), id); err != nil {
		return out, err
	}
	out.Status, err = readSubmission(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) RenewSubmission(ctx context.Context, id, token string, now time.Time, ttl time.Duration) (bool, error) {
	if id == "" || token == "" || !submissionTTL(now, ttl) {
		return false, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, owner, expires string
	var canceled bool
	if err := tx.QueryRowContext(ctx, "SELECT state,token,lease_expires_at,cancel_requested FROM submissions WHERE id=?", id).Scan(&state, &owner, &expires, &canceled); err != nil {
		return false, err
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || owner != token || state != "running" || !now.Before(at) {
		return false, submissions.ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET lease_expires_at=?,updated_at=? WHERE id=?", submissionTime(now.Add(ttl)), submissionTime(now), id); err != nil {
		return false, err
	}
	return canceled, tx.Commit()
}

func (s *Store) CancelSubmission(ctx context.Context, id string) (submissions.Status, error) {
	if !sessions.ValidEventPageID(id) {
		return submissions.Status{}, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return submissions.Status{}, err
	}
	defer tx.Rollback()
	status, err := readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	if status.State == "queued" || status.State == "running" {
		state := status.State
		if state == "queued" {
			state = "canceled"
		}
		if !status.CancelRequested {
			if _, err := tx.ExecContext(ctx, "UPDATE submissions SET state=?,cancel_requested=1,updated_at=? WHERE id=?", state, submissionTime(time.Now()), id); err != nil {
				return status, err
			}
		}
	}
	status, err = readSubmission(ctx, tx, id)
	if err != nil {
		return status, err
	}
	return status, tx.Commit()
}

func submissionAppendGate(ctx context.Context, tx *sql.Tx, event runtime.Event, id, token string) error {
	bound := event.Data.SubmissionID
	if event.Kind != runtime.TaskStarted {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(json_extract(body,'$.data.submission_id'),'') FROM events WHERE task_id=? AND sequence=1", event.TaskID).Scan(&bound); err != nil {
			return err
		}
	}
	if bound == "" && id == "" {
		return nil
	}
	if bound == "" || bound != id || token == "" {
		return runtime.ErrExecutionLeaseLost
	}
	var state, owner, expires string
	var canceled bool
	if err := tx.QueryRowContext(ctx, "SELECT state,token,lease_expires_at,cancel_requested FROM submissions WHERE id=?", id).Scan(&state, &owner, &expires, &canceled); err != nil {
		return runtime.ErrExecutionLeaseLost
	}
	if owner != token || state != "running" {
		return runtime.ErrExecutionLeaseLost
	}
	if event.Kind == runtime.ToolCompleted || event.Kind == runtime.TaskCanceled {
		return nil
	}
	if canceled {
		return runtime.ErrCancellationRequested
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().Before(at) {
		return runtime.ErrExecutionLeaseLost
	}
	if event.Kind == runtime.TaskStarted {
		if err := validateBranchTaskStart(ctx, tx, id, event); err != nil {
			return err
		}
		if err := validateResumeTaskStart(ctx, tx, id, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FinishSubmission(ctx context.Context, id, token, state, errorCode string, result *submissions.Result) (submissions.Status, error) {
	if id == "" || token == "" || (state != "succeeded" && state != "failed" && state != "canceled") {
		return submissions.Status{}, submissions.ErrInvalid
	}
	if state == "succeeded" {
		if errorCode != "" || result == nil || result.TaskID == "" || result.Turns < 1 {
			return submissions.Status{}, submissions.ErrInvalid
		}
	} else {
		if result != nil && result.Text != "" {
			return submissions.Status{}, submissions.ErrInvalid
		}
		switch errorCode {
		case "task_failed", "execution_failed", "canceled", "interrupted", "lease_lost", "admission_denied", "deadline_exceeded", "persistence_failed":
		default:
			return submissions.Status{}, submissions.ErrInvalid
		}
	}
	var encoded []byte
	var err error
	if result != nil {
		encoded, err = json.Marshal(result)
		if err != nil || len(encoded) > submissions.MaxRequestBytes || !utf8.ValidString(result.Text) || result.RouteEstimatedCost != nil && (math.IsNaN(*result.RouteEstimatedCost) || math.IsInf(*result.RouteEstimatedCost, 0) || *result.RouteEstimatedCost < 0) {
			return submissions.Status{}, submissions.ErrInvalid
		}
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return submissions.Status{}, err
	}
	defer tx.Rollback()
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT token FROM submissions WHERE id=?", id).Scan(&owner); err != nil {
		return submissions.Status{}, err
	}
	if owner != token {
		return submissions.Status{}, submissions.ErrLeaseLost
	}
	previous, err := readSubmission(ctx, tx, id)
	if err != nil {
		return previous, err
	}
	if previous.State != "running" {
		old, _ := json.Marshal(previous.Result)
		next, _ := json.Marshal(result)
		if previous.State == state && previous.ErrorCode == errorCode && string(old) == string(next) {
			return previous, tx.Commit()
		}
		return submissions.Status{}, submissions.ErrConflict
	}
	if state == "succeeded" && (previous.CancelRequested || previous.LeaseExpiresAt == nil || !time.Now().Before(*previous.LeaseExpiresAt)) {
		return submissions.Status{}, submissions.ErrLeaseLost
	}
	if result != nil {
		linked := map[string]bool{}
		for _, task := range previous.TaskIDs {
			linked[task] = true
		}
		if result.TaskID != "" && !linked[result.TaskID] {
			return submissions.Status{}, submissions.ErrInvalid
		}
		for _, task := range result.PreviousTaskIDs {
			if !linked[task] {
				return submissions.Status{}, submissions.ErrInvalid
			}
		}
		if state == "succeeded" {
			var taskState string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM task_heads WHERE task_id=?", result.TaskID).Scan(&taskState); err != nil || taskState != "completed" {
				return submissions.Status{}, submissions.ErrInvalid
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET state=?,error_code=?,result=?,updated_at=? WHERE id=?", state, errorCode, encoded, submissionTime(time.Now()), id); err != nil {
		return submissions.Status{}, err
	}
	out, err := readSubmission(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
