package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrLeaseRecovery = errors.New("terminal reader recovery unavailable")

type recoveryLease struct {
	Token, Task, Owner, Scope, Process string
	Expires                            int64
	Writer, Released                   int
	Reference                          processguard.Reference
}
type leaseRecoveryReceipt struct {
	Version         int       `json:"version"`
	Task            string    `json:"task"`
	Sequence        int64     `json:"sequence"`
	State           string    `json:"state"`
	Time            time.Time `json:"time"`
	Process         string    `json:"process"`
	Reason          string    `json:"reason"`
	Digest          string    `json:"digest"`
	CandidateDigest string    `json:"candidate_digest"`
	EventID         string    `json:"event_id,omitempty"`
	ChildTaskID     string    `json:"child_task_id,omitempty"`
	ChildSequence   int64     `json:"child_sequence,omitempty"`
	ChildEventID    string    `json:"child_event_id,omitempty"`
}
type recoveryQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRecoveryLease(ctx context.Context, q recoveryQuery, token string) (recoveryLease, error) {
	var c recoveryLease
	var task, owner, scope, pid sql.NullString
	var expiry, writer, released sql.NullInt64
	var legacy bool
	err := q.QueryRowContext(ctx, `SELECT
	CASE WHEN typeof(task_id)='text' AND length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END,
	CASE WHEN typeof(owner)='text' AND length(CAST(owner AS BLOB)) BETWEEN 1 AND 512 THEN owner END,
	CASE WHEN typeof(scope)='text' AND length(CAST(scope AS BLOB)) BETWEEN 1 AND 512 THEN scope END,
	process_id IS NULL,CASE WHEN typeof(process_id)='text' AND length(CAST(process_id AS BLOB)) BETWEEN 1 AND 128 THEN process_id END,
	CASE WHEN typeof(expires)='integer' THEN expires END,writer,released FROM resource_leases WHERE token=?`, token).Scan(&task, &owner, &scope, &legacy, &pid, &expiry, &writer, &released)
	if err != nil {
		return c, err
	}
	if !task.Valid || !sessions.ValidEventPageID(task.String) || !leaseObservationIdentity(owner) || !leaseObservationIdentity(scope) || !expiry.Valid || expiry.Int64 < 0 || time.Unix(0, expiry.Int64).UTC().Year() >= 2261 || !writer.Valid || writer.Int64 < 0 || writer.Int64 > 1 || !released.Valid || released.Int64 < 0 || released.Int64 > 1 {
		return c, ErrLeaseRecovery
	}
	c = recoveryLease{Token: token, Task: task.String, Owner: owner.String, Scope: scope.String, Expires: expiry.Int64, Writer: int(writer.Int64), Released: int(released.Int64)}
	if legacy {
		return c, nil
	}
	if !pid.Valid {
		return c, ErrLeaseRecovery
	}
	c.Process = pid.String
	var body []byte
	if err = q.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8192 THEN body END FROM lease_processes WHERE id=?`, c.Process).Scan(&body); err != nil {
		return c, ErrLeaseRecovery
	}
	if json.Unmarshal(body, &c.Reference) != nil || c.Reference.Validate() != nil || c.Reference.ID != c.Process {
		return c, ErrLeaseRecovery
	}
	canonical, _ := json.Marshal(c.Reference)
	if !bytes.Equal(body, canonical) {
		return c, ErrLeaseRecovery
	}
	return c, nil
}

func recoveryDigest(c recoveryLease, sequence int64, state string) string {
	c.Released = 0
	body, _ := json.Marshal(struct {
		Lease    recoveryLease
		Sequence int64
		State    string
	}{c, sequence, state})
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func existingLeaseRecovery(ctx context.Context, q recoveryQuery, c recoveryLease) (bool, error) {
	var body []byte
	var digest sql.NullString
	err := q.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 2048 THEN body END,CASE WHEN typeof(digest)='text' AND length(CAST(digest AS BLOB))=64 THEN digest END FROM lease_recoveries WHERE lease_token=?`, c.Token).Scan(&body, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrLeaseRecovery
	}
	var r leaseRecoveryReceipt
	if json.Unmarshal(body, &r) != nil {
		return false, ErrLeaseRecovery
	}
	canonical, _ := json.Marshal(r)
	var seq int64
	var state sql.NullString
	if q.QueryRowContext(ctx, `SELECT sequence,CASE WHEN typeof(state)='text' AND length(CAST(state AS BLOB)) BETWEEN 1 AND 16 THEN state END FROM task_heads WHERE task_id=?`, c.Task).Scan(&seq, &state) != nil || !state.Valid {
		return false, ErrLeaseRecovery
	}
	h := sha256.Sum256([]byte(c.Token))
	wantDigest := hex.EncodeToString(h[:])
	_, offset := r.Time.Zone()
	if seq < 1 || seq > 10000 || (state.String != "completed" && state.String != "failed" && state.String != "canceled") || offset != 0 || c.Process == "" {
		return false, ErrLeaseRecovery
	}
	if !bytes.Equal(body, canonical) || r.Version != 1 || r.Task != c.Task || r.Process != c.Process || r.Sequence != seq || r.State != state.String || r.Time.Year() < 1970 || r.Time.Year() >= 2261 || !digest.Valid || digest.String != wantDigest || r.Digest != wantDigest || r.CandidateDigest != recoveryDigest(c, seq, state.String) || c.Released != 1 || c.Writer != 0 {
		return false, ErrLeaseRecovery
	}
	switch r.Reason {
	case "terminal_reader_owner_unlocked":
		if r.EventID != "" || r.ChildTaskID != "" || r.ChildSequence != 0 || r.ChildEventID != "" {
			return false, ErrLeaseRecovery
		}
	case "orphan_worker_owner_unlocked", "orphan_worker_without_child_unlocked":
		if !sessions.ValidEventPageID(r.EventID) || state.String != "failed" {
			return false, ErrLeaseRecovery
		}
		var encoded []byte
		var terminal runtime.Event
		if q.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(body AS BLOB)) BETWEEN 1 AND 8388608 THEN body END FROM events WHERE task_id=? AND sequence=? AND id=?`, c.Task, seq, r.EventID).Scan(&encoded) != nil || json.Unmarshal(encoded, &terminal) != nil || terminal.Validate() != nil || terminal.TaskID != c.Task || terminal.ID != r.EventID || terminal.Sequence != seq || terminal.WorkerID != c.Owner || terminal.Kind != runtime.TaskFailed || terminal.Data.Code != "worker_owner_interrupted" || !terminal.Time.Equal(r.Time) {
			return false, ErrLeaseRecovery
		}
		if r.Reason == "orphan_worker_without_child_unlocked" {
			canonicalTerminal, encodeErr := terminal.Encode()
			var child bool
			if r.ChildTaskID != "" || r.ChildSequence != 0 || r.ChildEventID != "" || encodeErr != nil || !bytes.Equal(encoded, canonicalTerminal) || q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE json_extract(body,'$.data.parent_task_id')=?)`, c.Task).Scan(&child) != nil || child {
				return false, ErrLeaseRecovery
			}
			if err := validateOrphanWithoutChildReceipt(ctx, q, r); err != nil {
				return false, err
			}
		}
		if err := validateOrphanChildReceipt(ctx, q, r); err != nil {
			return false, err
		}
	default:
		return false, ErrLeaseRecovery
	}
	return true, nil
}

// RecoverTerminalReader releases only a terminal, effect-resolved reader whose
// original process lock is demonstrably unlocked. It never changes a journal,
// executes work, repairs missing guards, or treats lease expiry as owner death.
func (s *Store) RecoverTerminalReader(ctx context.Context, token string, now time.Time) (bool, error) {
	now = now.UTC()
	if ctx == nil || len(token) == 0 || len(token) > 512 || !leaseObservationIdentity(sql.NullString{String: token, Valid: true}) || now.Year() < 1970 || now.Year() >= 2261 {
		return false, ErrLeaseRecovery
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := readRecoveryLease(ctx, s.db, token)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrLeaseRecovery
	}
	if c.Released == 1 {
		_, err := existingLeaseRecovery(ctx, s.db, c)
		return false, err
	}
	if c.Released != 0 || c.Writer != 0 || c.Process == "" {
		return false, nil
	}
	observation, err := processguard.Probe(ctx, c.Reference)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	defer observation.Close()
	if observation.State != processguard.Unlocked {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE resource_leases SET owner=owner WHERE token=?`, token); err != nil {
		return false, ErrLeaseRecovery
	}
	if observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrLeaseRecovery
	}
	current, err := readRecoveryLease(ctx, tx, token)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	if receipt, err := existingLeaseRecovery(ctx, tx, current); err != nil || receipt {
		return false, err
	}
	if current != c {
		return false, nil
	}
	snapshot, err := taskSnapshot(ctx, tx, c.Task)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	if (snapshot.State != "completed" && snapshot.State != "failed" && snapshot.State != "canceled") || len(snapshot.Pending) != 0 || snapshot.UncertainEffects || snapshot.InterruptedTurn {
		return false, nil
	}
	h := sha256.Sum256([]byte(c.Token))
	r := leaseRecoveryReceipt{Version: 1, Task: c.Task, Sequence: snapshot.Sequence, State: snapshot.State, Time: now.UTC(), Process: c.Process, Reason: "terminal_reader_owner_unlocked", Digest: hex.EncodeToString(h[:]), CandidateDigest: recoveryDigest(c, snapshot.Sequence, snapshot.State)}
	body, _ := json.Marshal(r)
	if _, err = tx.ExecContext(ctx, `INSERT INTO lease_recoveries(lease_token,digest,body) VALUES(?,?,?)`, token, r.Digest, body); err != nil {
		return false, ErrLeaseRecovery
	}
	result, err := tx.ExecContext(ctx, `UPDATE resource_leases SET released=1 WHERE token=? AND task_id=? AND owner=? AND process_id=? AND writer=0 AND released=0`, token, c.Task, c.Owner, c.Process)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, ErrLeaseRecovery
	}
	final, err := readRecoveryLease(ctx, tx, token)
	expected := c
	expected.Released = 1
	if err != nil || final != expected {
		return false, ErrLeaseRecovery
	}
	if observation.ConfirmUnlocked(ctx) != nil {
		return false, ErrLeaseRecovery
	}
	if tx.Commit() != nil {
		return false, ErrLeaseRecovery
	}
	return true, nil
}
