package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// Approval transactions acquire the SQLite writer lock before observing state.
// Consumption is deliberately not idempotent: acknowledgement loss must never
// authorize replay of an effect whose outcome is unknown.
func (s *Store) approvalTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, approvals.ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tool_approvals SET state=state WHERE 0`); err != nil {
		tx.Rollback()
		return nil, approvals.ErrUnavailable
	}
	return tx, nil
}

func readApproval(ctx context.Context, tx *sql.Tx, id string) (approvals.Record, error) {
	var r approvals.Record
	var task, call, state string
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT task_id,tool_call_id,state,CASE WHEN length(body)<=65536 THEN body END FROM tool_approvals WHERE id=? AND length(task_id)<=128 AND length(tool_call_id)<=128 AND length(state)<=16`, id).Scan(&task, &call, &state, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return r, sql.ErrNoRows
	}
	if err != nil {
		return r, approvals.ErrUnavailable
	}
	if len(body) == 0 || json.Unmarshal(body, &r) != nil || r.Validate() != nil || r.Request.ID != id || r.Request.TaskID != task || r.Request.ToolCallID != call || r.State != state {
		return approvals.Record{}, approvals.ErrInvalid
	}
	return r, nil
}

func sameApproval(a, b approvals.Request) bool {
	return a.Matches(b)
}

func commitApproval(tx *sql.Tx, r approvals.Record) (approvals.Record, error) {
	if err := tx.Commit(); err != nil {
		return approvals.Record{}, approvals.ErrUnavailable
	}
	return r, nil
}

func saveApproval(ctx context.Context, tx *sql.Tx, r approvals.Record, insert bool) error {
	if r.Validate() != nil {
		return approvals.ErrInvalid
	}
	body, err := json.Marshal(r)
	if err != nil || len(body) > 65536 {
		return approvals.ErrInvalid
	}
	if insert {
		_, err = tx.ExecContext(ctx, `INSERT INTO tool_approvals(id,task_id,tool_call_id,state,body) VALUES(?,?,?,?,?)`, r.Request.ID, r.Request.TaskID, r.Request.ToolCallID, r.State, body)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE tool_approvals SET state=?,body=? WHERE id=?`, r.State, body, r.Request.ID)
	}
	if err != nil {
		return approvals.ErrUnavailable
	}
	return nil
}

// approvalCall requires the currently outstanding call at the durable head.
// ToolStarted omits arguments; the earlier completed turn contains redacted
// proposals. Replay establishes call identity, not raw argument equivalence.
// The trusted coordinator binds the exact argument digest in Request.
func approvalCall(ctx context.Context, tx *sql.Tx, r approvals.Request) error {
	status, err := cancellationStatus(ctx, tx, r.TaskID, false)
	if err != nil {
		return approvals.ErrUnavailable
	}
	if status.State != "running" || status.Requested {
		return approvals.ErrConflict
	}
	snapshot, err := taskSnapshot(ctx, tx, r.TaskID)
	if err != nil {
		return approvals.ErrInvalid
	}
	pending, ok := snapshot.Pending[r.ToolCallID]
	if snapshot.State != "running" || !ok || !pending.Dispatched || pending.TurnID != r.TurnID || pending.Call.Name != r.ToolName || pending.ToolBehavior != r.ToolBehavior {
		return approvals.ErrConflict
	}
	var body []byte
	var seq int64
	var session, eventID string
	err = tx.QueryRowContext(ctx, `SELECT e.sequence,h.session_id,e.id,CASE WHEN length(e.body)<=1048576 THEN e.body END FROM task_heads h JOIN events e ON e.task_id=h.task_id AND e.sequence=h.sequence WHERE h.task_id=? AND length(h.session_id)<=128 AND length(e.id)<=128`, r.TaskID).Scan(&seq, &session, &eventID, &body)
	if err != nil {
		return approvals.ErrUnavailable
	}
	var event runtime.Event
	if len(body) == 0 || json.Unmarshal(body, &event) != nil || event.Validate() != nil || event.ID != eventID || event.TaskID != r.TaskID || event.SessionID != session || event.Sequence != seq {
		return approvals.ErrInvalid
	}
	if event.Kind != runtime.ToolStarted || event.TurnID != r.TurnID || event.Data.ToolCallID != r.ToolCallID || event.Data.ToolName != r.ToolName || event.Data.Effect != runtime.UncertainEffect || event.Data.ToolBehavior != r.ToolBehavior {
		return approvals.ErrConflict
	}
	return nil
}

func (s *Store) RequestApproval(ctx context.Context, req approvals.Request) (approvals.Record, error) {
	if req.Validate() != nil {
		return approvals.Record{}, approvals.ErrInvalid
	}
	tx, err := s.approvalTx(ctx)
	if err != nil {
		return approvals.Record{}, err
	}
	defer tx.Rollback()
	prior, err := readApproval(ctx, tx, req.ID)
	if err == nil {
		if !sameApproval(prior.Request, req) {
			return approvals.Record{}, approvals.ErrConflict
		}
		return commitApproval(tx, prior)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return approvals.Record{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_approvals WHERE task_id=? AND tool_call_id=?`, req.TaskID, req.ToolCallID).Scan(&count); err != nil {
		return approvals.Record{}, approvals.ErrUnavailable
	}
	if count != 0 {
		return approvals.Record{}, approvals.ErrConflict
	}
	if err = approvalCall(ctx, tx, req); err != nil {
		return approvals.Record{}, err
	}
	r := approvals.Record{Request: req, State: "pending"}
	if err = saveApproval(ctx, tx, r, true); err != nil {
		return approvals.Record{}, err
	}
	return commitApproval(tx, r)
}

func (s *Store) ReadApproval(ctx context.Context, id string) (approvals.Record, error) {
	if !sessions.ValidEventPageID(id) {
		return approvals.Record{}, approvals.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return approvals.Record{}, approvals.ErrUnavailable
	}
	defer tx.Rollback()
	r, err := readApproval(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		err = approvals.ErrUnavailable
	}
	if err != nil {
		return approvals.Record{}, err
	}
	return commitApproval(tx, r)
}

func (s *Store) DecideApproval(ctx context.Context, id string, d approvals.Decision) (approvals.Record, error) {
	if !sessions.ValidEventPageID(id) || d.Validate() != nil {
		return approvals.Record{}, approvals.ErrInvalid
	}
	tx, err := s.approvalTx(ctx)
	if err != nil {
		return approvals.Record{}, err
	}
	defer tx.Rollback()
	r, err := readApproval(ctx, tx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = approvals.ErrUnavailable
		}
		return approvals.Record{}, err
	}
	for _, prior := range r.Decisions {
		if prior.ID == d.ID {
			if prior.Actor != d.Actor || prior.Allowed != d.Allowed || !prior.Time.Equal(d.Time) {
				return approvals.Record{}, approvals.ErrConflict
			}
			return commitApproval(tx, r)
		}
	}
	if d.Time.Before(r.Request.CreatedAt) || !d.Time.Before(r.Request.ExpiresAt) {
		return approvals.Record{}, approvals.ErrConflict
	}
	if err = approvalCall(ctx, tx, r.Request); err != nil {
		return approvals.Record{}, err
	}
	switch {
	case r.State == "pending" && d.Allowed:
		r.State = "approved"
	case r.State == "pending" && !d.Allowed:
		r.State = "denied"
	case r.State == "approved" && !d.Allowed:
		r.State = "revoked"
	default:
		return approvals.Record{}, approvals.ErrConflict
	}
	r.Decisions = append(r.Decisions, d)
	if err = saveApproval(ctx, tx, r, false); err != nil {
		return approvals.Record{}, err
	}
	return commitApproval(tx, r)
}

func (s *Store) ConsumeApproval(ctx context.Context, req approvals.Request, token, owner string, now time.Time) (approvals.Record, error) {
	_, offset := now.Zone()
	if req.Validate() != nil || token == "" || owner == "" || len(token) > 512 || len(owner) > 512 || now.Year() < 1970 || now.Year() >= 2261 || offset != 0 {
		return approvals.Record{}, approvals.ErrInvalid
	}
	tx, err := s.approvalTx(ctx)
	if err != nil {
		return approvals.Record{}, err
	}
	defer tx.Rollback()
	r, err := readApproval(ctx, tx, req.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = approvals.ErrUnavailable
		}
		return approvals.Record{}, err
	}
	if r.State != "approved" || !sameApproval(r.Request, req) || now.Before(req.CreatedAt) || !now.Before(req.ExpiresAt) {
		return approvals.Record{}, approvals.ErrConflict
	}
	if err = approvalCall(ctx, tx, req); err != nil {
		return approvals.Record{}, err
	}
	var count int
	if err := leaseProcessGate(ctx, tx, token, owner); err != nil {
		return approvals.Record{}, approvals.ErrConflict
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM resource_leases WHERE token=? AND owner=? AND task_id=? AND scope=? AND writer=1 AND released=0 AND expires>?`, token, owner, req.TaskID, req.Scope, now.UnixNano()).Scan(&count)
	if err != nil {
		return approvals.Record{}, approvals.ErrUnavailable
	}
	if count != 1 {
		return approvals.Record{}, approvals.ErrConflict
	}
	now = now.UTC()
	r.State = "consumed"
	r.ConsumedAt = &now
	if err = saveApproval(ctx, tx, r, false); err != nil {
		return approvals.Record{}, err
	}
	return commitApproval(tx, r)
}
