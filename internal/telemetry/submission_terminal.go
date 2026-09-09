package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

// RecoverTerminalSubmission projects committed terminal histories only. It never
// dispatches inference, tools, fallback, evaluation, or audits.
func (s *Store) RecoverTerminalSubmission(ctx context.Context, id, configDigest string, now time.Time) (bool, error) {
	if !sessions.ValidEventPageID(id) || !submissionDigest(configDigest) || now.IsZero() {
		return false, submissions.ErrInvalid
	}
	tx, err := submissionTx(ctx, s)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, config, token, expiry string
	var canceled int
	err = tx.QueryRowContext(ctx, `SELECT state,config_digest,token,lease_expires_at,cancel_requested FROM submissions WHERE id=? AND length(CAST(state AS BLOB))<=16 AND length(CAST(config_digest AS BLOB))=64 AND length(CAST(token AS BLOB))<=128 AND length(CAST(lease_expires_at AS BLOB))<=64`, id).Scan(&state, &config, &token, &expiry, &canceled)
	if err != nil {
		return false, err
	}
	if state != "running" || config != configDigest {
		return false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !sessions.ValidEventPageID(token) || canceled < 0 || canceled > 1 {
		return false, submissions.ErrInvalid
	}
	if now.Before(at) {
		return false, nil
	}
	if err = validateBranchRecoveryTx(ctx, tx, id); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB))<=128 THEN task_id END FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=? ORDER BY rowid LIMIT 67`, id)
	if err != nil {
		return false, err
	}
	ids := []string{}
	for rows.Next() {
		var task sql.NullString
		if err = rows.Scan(&task); err != nil {
			rows.Close()
			return false, err
		}
		if !task.Valid || !sessions.ValidEventPageID(task.String) {
			rows.Close()
			return false, submissions.ErrInvalid
		}
		ids = append(ids, task.String)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(ids) == 0 || len(ids) > 66 {
		return false, nil
	}
	histories := make([][]runtime.Event, 0, len(ids))
	var bytes, eventCount int64
	for _, task := range ids {
		var size, events int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(body AS BLOB))),0),count(*) FROM events WHERE task_id=?`, task).Scan(&size, &events); err != nil {
			return false, err
		}
		if size < 1 || events < 1 || size > (8<<20)-bytes || events > 10000-eventCount {
			return false, submissions.ErrInvalid
		}
		bytes += size
		eventCount += events
		history, complete, e := terminalSubmissionHistory(ctx, tx, task)
		if e != nil {
			return false, e
		}
		if !complete {
			return false, nil
		}
		if history[0].Data.SubmissionID != id {
			return false, submissions.ErrInvalid
		}
		histories = append(histories, history)
	}
	outcome, err := sessions.ProjectTerminalTree(histories)
	if err != nil {
		return false, err
	}
	// External continuation parents must come from the original intake, not
	// from treating an orphaned inference child as a new root.
	var continuation sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT CASE
		WHEN json_type(request,'$.request.ContinueTaskID') IS NULL THEN ''
		WHEN json_type(request,'$.request.ContinueTaskID')='text' AND length(CAST(json_extract(request,'$.request.ContinueTaskID') AS BLOB))<=128
		THEN json_extract(request,'$.request.ContinueTaskID') END FROM submissions WHERE id=?`, id).Scan(&continuation); err != nil {
		return false, err
	}
	if !continuation.Valid || (continuation.String != "" && !sessions.ValidEventPageID(continuation.String)) {
		return false, submissions.ErrInvalid
	}
	rootIDs := map[string]bool{}
	if outcome.Result != nil {
		rootIDs[outcome.Result.TaskID] = true
		for _, previous := range outcome.Result.PreviousTaskIDs {
			rootIDs[previous] = true
		}
	}
	for _, history := range histories {
		if rootIDs[history[0].TaskID] && history[0].Data.ParentTaskID != continuation.String {
			return false, submissions.ErrInvalid
		}
	}
	if outcome.Result != nil {
		outcome.Result.AuditStatus = "not_recovered"
		outcome.Result.AuditID = ""
	}
	reason := "terminal_history"
	if canceled == 1 {
		outcome.State = "canceled"
		outcome.ErrorCode = "canceled"
		reason = "cancellation_requested"
		if outcome.Result != nil {
			outcome.Result.Text = ""
		}
	}
	body, err := json.Marshal(outcome.Result)
	if err != nil || len(body) > submissions.MaxRequestBytes {
		return false, submissions.ErrInvalid
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM submission_recoveries WHERE submission_id=?`, id).Scan(&count); err != nil {
		return false, err
	}
	if count >= 4 {
		return false, submissions.ErrInvalid
	}
	recovery := submissions.Recovery{Version: 1, ID: rand.Text(), SubmissionID: id, Time: now.UTC(), Action: outcome.State, Reason: reason}
	if recovery.Validate() != nil {
		return false, submissions.ErrInvalid
	}
	audit, err := json.Marshal(recovery)
	if err != nil {
		return false, submissions.ErrInvalid
	}
	digest := sha256.Sum256([]byte(token))
	if _, err = tx.ExecContext(ctx, `INSERT INTO submission_recoveries VALUES(?,?,?,?)`, recovery.ID, id, hex.EncodeToString(digest[:]), audit); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE submissions SET state=?,token='',lease_expires_at='',updated_at=?,result=?,error_code=? WHERE id=?`, outcome.State, submissionTime(now), body, outcome.ErrorCode, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func terminalSubmissionHistory(ctx context.Context, tx *sql.Tx, task string) ([]runtime.Event, bool, error) {
	var session, state string
	var head int64
	err := tx.QueryRowContext(ctx, `SELECT session_id,state,sequence FROM task_heads WHERE task_id=? AND length(CAST(session_id AS BLOB))<=128 AND length(CAST(state AS BLOB))<=16`, task).Scan(&session, &state, &head)
	if err != nil {
		return nil, false, submissions.ErrInvalid
	}
	if state == "running" {
		return nil, false, nil
	}
	if head < 1 || head > 10000 || !sessions.ValidEventPageID(session) {
		return nil, false, submissions.ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END,sequence,length(CAST(body AS BLOB)) FROM events WHERE task_id=? ORDER BY sequence LIMIT 10001`, task)
	if err != nil {
		return nil, false, err
	}
	type entry struct {
		id  string
		seq int64
	}
	entries := []entry{}
	var total int64
	for rows.Next() {
		var eid sql.NullString
		var seq, size int64
		if err = rows.Scan(&eid, &seq, &size); err != nil {
			rows.Close()
			return nil, false, err
		}
		total += size
		if !eid.Valid || size < 1 || total > 8<<20 || seq != int64(len(entries)+1) || len(entries) >= 10000 {
			rows.Close()
			return nil, false, submissions.ErrInvalid
		}
		entries = append(entries, entry{eid.String, seq})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if int64(len(entries)) != head {
		return nil, false, submissions.ErrInvalid
	}
	history := make([]runtime.Event, 0, len(entries))
	for _, entry := range entries {
		var body []byte
		if err = tx.QueryRowContext(ctx, `SELECT body FROM events WHERE task_id=? AND sequence=?`, task, entry.seq).Scan(&body); err != nil {
			return nil, false, err
		}
		var e runtime.Event
		if json.Unmarshal(body, &e) != nil || e.Validate() != nil || e.ID != entry.id || e.Sequence != entry.seq || e.TaskID != task || e.SessionID != session || e.CorrelationID != task {
			return nil, false, submissions.ErrInvalid
		}
		history = append(history, e)
	}
	if !sessions.EventPageStateMatches(state, history[len(history)-1].Kind) {
		return nil, false, submissions.ErrInvalid
	}
	return history, true, nil
}
