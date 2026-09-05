package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"darwinrouter/sessions"
	"darwinrouter/submissions"
)

// RecoverUndispatched never reclaims a submission with a durable task start.
func (s *Store) RecoverUndispatched(ctx context.Context, id, configDigest string, now time.Time) (bool, error) {
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
	var started bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.submission_id')=?)`, id).Scan(&started); err != nil {
		return false, err
	}
	if started {
		return false, nil
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM submission_recoveries WHERE submission_id=?`, id).Scan(&count); err != nil {
		return false, err
	}
	if count > 3 {
		return false, submissions.ErrInvalid
	}
	action, reason, code := "queued", "lease_expired_no_task", ""
	if canceled == 1 {
		action, reason, code = "canceled", "cancellation_requested", "canceled"
	} else if count >= 3 {
		action, reason, code = "failed", "recovery_limit", "recovery_exhausted"
	} else {
		var queued int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE state='queued'`).Scan(&queued); err != nil {
			return false, err
		}
		if queued >= submissions.MaxQueued {
			return false, nil
		}
	}
	record := submissions.Recovery{Version: 1, ID: rand.Text(), SubmissionID: id, Time: now.UTC(), Action: action, Reason: reason}
	if record.Validate() != nil {
		return false, submissions.ErrInvalid
	}
	body, err := json.Marshal(record)
	if err != nil {
		return false, submissions.ErrInvalid
	}
	digest := sha256.Sum256([]byte(token))
	if _, err = tx.ExecContext(ctx, `INSERT INTO submission_recoveries VALUES(?,?,?,?)`, record.ID, id, hex.EncodeToString(digest[:]), body); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE submissions SET state=?,token='',lease_expires_at='',updated_at=?,result=NULL,error_code=? WHERE id=?`, action, submissionTime(now), code, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RecoveryHistory(ctx context.Context, id string) ([]submissions.Recovery, error) {
	out := []submissions.Recovery{}
	if !sessions.ValidEventPageID(id) {
		return nil, submissions.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version < 12 {
		return nil, sql.ErrNoRows
	}
	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM submissions WHERE id=?`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if version < 13 {
		return out, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(id AS BLOB))<=128 THEN id END, CASE WHEN length(body)<=4096 THEN body END FROM submission_recoveries WHERE submission_id=? ORDER BY rowid LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var recordID sql.NullString
		var body []byte
		if err = rows.Scan(&recordID, &body); err != nil {
			rows.Close()
			return nil, err
		}
		var r submissions.Recovery
		if !recordID.Valid || json.Unmarshal(body, &r) != nil || r.Validate() != nil || r.ID != recordID.String || r.SubmissionID != id || len(out) == 4 {
			rows.Close()
			return nil, submissions.ErrInvalid
		}
		canonical, marshalErr := json.Marshal(r)
		if marshalErr != nil || !bytes.Equal(canonical, body) {
			rows.Close()
			return nil, submissions.ErrInvalid
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
