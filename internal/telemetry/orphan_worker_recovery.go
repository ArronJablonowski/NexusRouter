package telemetry

import (
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

// RecoverOrphanWorker records failure, never acceptance, for a stopped local
// worker whose child is terminal or a provable model/read-only interruption.
// Ownership proof, event, projection, reader release and receipt share one commit.
func (s *Store) RecoverOrphanWorker(ctx context.Context, token string, now time.Time) (bool, error) {
	now = now.UTC()
	if ctx == nil || len(token) > 512 || !leaseObservationIdentity(sql.NullString{String: token, Valid: true}) || now.Year() < 1970 || now.Year() >= 2261 {
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
		_, err = existingLeaseRecovery(ctx, s.db, c)
		return false, err
	}
	if c.Writer != 0 || c.Process == "" {
		return false, nil
	}
	proof, err := processguard.Probe(ctx, c.Reference)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	defer proof.Close()
	if proof.ConfirmUnlocked(ctx) != nil {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE resource_leases SET owner=owner WHERE token=?`, token); err != nil || proof.ConfirmUnlocked(ctx) != nil {
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
	histories, err := orphanWorkerHistories(ctx, tx, c.Task)
	if errors.Is(err, sessions.ErrHistory) {
		return false, nil
	}
	if err != nil {
		return false, ErrLeaseRecovery
	}
	tree, err := sessions.PlanInterruptedWorkerTree(histories, now)
	if errors.Is(err, sessions.ErrHistory) {
		return false, nil
	}
	plan := tree.Worker
	if err != nil || len(plan.Events) != 1 || plan.ParentTaskID != c.Task || histories[0][0].WorkerID != c.Owner {
		return false, ErrLeaseRecovery
	}
	withoutChild := len(histories) == 1
	parent, err := orphanWorkerParent(ctx, tx, histories[0][0], now, withoutChild)
	if err != nil {
		return false, ErrLeaseRecovery
	}
	event := plan.Events[0]
	if uniqueWorkerLease(ctx, tx, event, token, c.Owner) != nil {
		return false, ErrLeaseRecovery
	}
	// Different logical owners on the same task are ambiguous too. This query
	// bounds returned rows, not the total scan cost of a damaged database.
	var count int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT token FROM resource_leases WHERE task_id=? LIMIT 2)`, c.Task).Scan(&count) != nil || count != 1 {
		return false, ErrLeaseRecovery
	}
	var childReaders []recoveryLease
	toolRecovery := tree.Child != nil && len(tree.Child.Events) > 1 && tree.Child.Events[len(tree.Child.Events)-1].Data.Code == "interrupted_read_only_tool"
	if tree.Child != nil {
		// Pending explicitly read-only dispatches may retain bounded readers from
		// this exact stopped process image. Other child plans still require no
		// held tool leases. This transaction never releases child readers.
		var held bool
		if tree.Child.ParentTaskID != histories[1][0].TaskID || len(tree.Child.Events) < 1 || (!toolRecovery && len(tree.Child.Events) != 1) {
			return false, ErrLeaseRecovery
		}
		if toolRecovery {
			childReaders, err = orphanToolReaders(ctx, tx, tree.Child.ParentTaskID, c)
			if err != nil {
				return false, ErrLeaseRecovery
			}
		} else if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_leases WHERE task_id=? AND released=0)`, tree.Child.ParentTaskID).Scan(&held) != nil || held {
			return false, ErrLeaseRecovery
		}
		if appendOrphanFailure(ctx, tx, *tree.Child) != nil {
			return false, ErrLeaseRecovery
		}
	}
	if appendOrphanFailure(ctx, tx, plan) != nil {
		return false, ErrLeaseRecovery
	}
	h := sha256.Sum256([]byte(token))
	r := leaseRecoveryReceipt{Version: 1, Task: c.Task, Sequence: event.Sequence, State: "failed", Time: now, Process: c.Process, Reason: "orphan_worker_owner_unlocked", Digest: hex.EncodeToString(h[:]), CandidateDigest: recoveryDigest(c, event.Sequence, "failed"), EventID: event.ID}
	if withoutChild {
		r.Reason = "orphan_worker_without_child_unlocked"
	}
	if tree.Child != nil {
		last := tree.Child.Events[len(tree.Child.Events)-1]
		r.ChildTaskID, r.ChildSequence, r.ChildEventID = tree.Child.ParentTaskID, last.Sequence, last.ID
	}
	receipt, _ := json.Marshal(r)
	if _, err = tx.ExecContext(ctx, `INSERT INTO lease_recoveries(lease_token,digest,body) VALUES(?,?,?)`, token, r.Digest, receipt); err != nil || releaseFinishedWorker(ctx, tx, event, token, c.Owner) != nil {
		return false, ErrLeaseRecovery
	}
	// Replay after all writes catches budget overflow and trigger-time journal
	// drift. The canonical source prefix and execution child must remain equal.
	finalHistories, err := orphanWorkerHistories(ctx, tx, c.Task)
	if err != nil || !orphanHistoriesMatch(histories, finalHistories, event, tree.Child) {
		return false, ErrLeaseRecovery
	}
	if tree.Child != nil {
		var held bool
		if toolRecovery {
			finalReaders, readErr := orphanToolReaders(ctx, tx, tree.Child.ParentTaskID, c)
			if readErr != nil || !sameOrphanToolReaders(childReaders, finalReaders) {
				return false, ErrLeaseRecovery
			}
		} else if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_leases WHERE task_id=? AND released=0)`, tree.Child.ParentTaskID).Scan(&held) != nil || held {
			return false, ErrLeaseRecovery
		}
	}
	finalParent, err := orphanWorkerParent(ctx, tx, histories[0][0], now, withoutChild)
	a, _ := json.Marshal(parent)
	b, _ := json.Marshal(finalParent)
	if err != nil || string(a) != string(b) {
		return false, ErrLeaseRecovery
	}
	final, err := readRecoveryLease(ctx, tx, token)
	expected := c
	expected.Released = 1
	if err != nil || final != expected || proof.ConfirmUnlocked(ctx) != nil {
		return false, ErrLeaseRecovery
	}
	if ok, err := existingLeaseRecovery(ctx, tx, final); err != nil || !ok {
		return false, ErrLeaseRecovery
	}
	if err = tx.Commit(); err != nil {
		return false, ErrLeaseRecovery
	}
	return true, nil
}

func appendOrphanFailure(ctx context.Context, tx *sql.Tx, plan sessions.InterruptionRecovery) error {
	if len(plan.Events) < 1 || len(plan.Events) > 33 {
		return ErrLeaseRecovery
	}
	event := plan.Events[len(plan.Events)-1]
	if event.Kind != runtime.TaskFailed || event.TaskID != plan.ParentTaskID || event.Sequence != plan.ExpectedSequence+int64(len(plan.Events)) {
		return ErrLeaseRecovery
	}
	submission, err := taskSubmissionID(ctx, tx, event.TaskID)
	if err != nil {
		return err
	}
	for i, e := range plan.Events {
		if e.TaskID != event.TaskID || e.SessionID != event.SessionID || e.Sequence != plan.ExpectedSequence+int64(i)+1 || i < len(plan.Events)-1 && e.Kind != runtime.ToolCompleted {
			return ErrLeaseRecovery
		}
		body, err := e.Encode()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events VALUES(?,?,?,?)`, e.ID, e.TaskID, e.Sequence, body); err != nil {
			return err
		}
		if err = appendEventLog(ctx, tx, e, body); err != nil {
			return err
		}
		if err = appendSubmissionStreamEvent(ctx, tx, e, body, submission); err != nil {
			return err
		}
		if err = appendTaskTiming(ctx, tx, e); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_heads SET sequence=?,state='failed' WHERE task_id=? AND session_id=? AND sequence=? AND state='running'`, event.Sequence, event.TaskID, event.SessionID, plan.ExpectedSequence)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrLeaseRecovery
	}
	return nil
}

// Bind the worker's origin to the parent's actual dispatched delegation. This
// check does not authorize parent continuation or alter its journal/ownership.
func orphanWorkerParent(ctx context.Context, tx *sql.Tx, start runtime.Event, now time.Time, requireReadOnly bool) ([]runtime.Event, error) {
	var history []runtime.Event
	snapshot, err := taskSnapshotWithEvents(ctx, tx, start.Data.ParentTaskID, &history)
	if err != nil || len(history) == 0 || history[0].WorkerID != "" || history[0].SessionID != start.SessionID || history[0].Data.SubmissionID != start.Data.SubmissionID {
		return nil, ErrLeaseRecovery
	}
	origin := start.Data.DelegationOrigin
	if origin == nil || origin.Validate() != nil {
		return nil, ErrLeaseRecovery
	}
	pending, ok := snapshot.Pending[origin.ToolCallID]
	if !ok || !pending.Dispatched || pending.TurnID != origin.TurnID || pending.AttemptID != origin.AttemptID || pending.Call.Name != origin.ToolName || (pending.ToolBehavior != "" && pending.ToolBehavior != runtime.BehaviorReadOnly) {
		return nil, ErrLeaseRecovery
	}
	if requireReadOnly && pending.ToolBehavior != runtime.BehaviorReadOnly {
		return nil, ErrLeaseRecovery
	}
	if origin.ToolName == "delegate_batch" {
		var args struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if json.Unmarshal(pending.Call.Arguments, &args) != nil || len(args.Tasks) < 2 || len(args.Tasks) > 4 || origin.BatchIndex == nil || *origin.BatchIndex >= len(args.Tasks) {
			return nil, ErrLeaseRecovery
		}
	}
	for _, event := range history {
		if event.Time.After(now) {
			return nil, ErrLeaseRecovery
		}
	}
	return history, nil
}

func orphanHistoriesMatch(before, after [][]runtime.Event, event runtime.Event, child *sessions.InterruptionRecovery) bool {
	if (len(before) != 1 && len(before) != 2) || len(after) != len(before) || len(after[0]) != len(before[0])+1 || len(before) == 1 && child != nil {
		return false
	}
	want := [][]runtime.Event{append(append([]runtime.Event(nil), before[0]...), event)}
	if len(before) == 2 {
		want = append(want, before[1])
	}
	if child != nil {
		want[1] = append(append([]runtime.Event(nil), before[1]...), child.Events...)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(after)
	return string(a) == string(b)
}

// Zero or one direct, leaf execution child is admitted. Each raw journal and their
// aggregate are bounded before loading payloads; nested work is unsupported.
func orphanWorkerHistories(ctx context.Context, tx *sql.Tx, worker string) ([][]runtime.Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(CAST(task_id AS BLOB)) BETWEEN 1 AND 128 THEN task_id END FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.parent_task_id')=? LIMIT 2`, worker)
	if err != nil {
		return nil, err
	}
	ids := []string{worker}
	for rows.Next() {
		var id sql.NullString
		if rows.Scan(&id) != nil || !id.Valid || !sessions.ValidEventPageID(id.String) || len(ids) != 1 || id.String == worker {
			rows.Close()
			return nil, ErrLeaseRecovery
		}
		ids = append(ids, id.String)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if rows.Close() != nil {
		return nil, ErrLeaseRecovery
	}
	if len(ids) == 1 {
		// A malformed/non-start linked event is contradictory evidence, not
		// proof that no execution child was created.
		var linked bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE json_extract(body,'$.data.parent_task_id')=?)`, worker).Scan(&linked) != nil || linked {
			return nil, ErrLeaseRecovery
		}
	}
	if len(ids) == 2 {
		var nested bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE json_extract(body,'$.kind')='task.started' AND json_extract(body,'$.data.parent_task_id')=?)`, ids[1]).Scan(&nested) != nil {
			return nil, ErrLeaseRecovery
		}
		if nested {
			return nil, sessions.ErrHistory
		}
	}
	var bytes, count int64
	histories := make([][]runtime.Event, len(ids))
	for i, id := range ids {
		var size, events int64
		if tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(body AS BLOB))),0),count(*) FROM events WHERE task_id=?`, id).Scan(&size, &events) != nil || size < 1 || events < 1 || size > (8<<20)-bytes || events > 10000-count {
			return nil, ErrLeaseRecovery
		}
		bytes += size
		count += events
		if _, err = taskSnapshotWithEvents(ctx, tx, id, &histories[i]); err != nil {
			return nil, err
		}
	}
	return histories, nil
}
