package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

var errTaskTiming = errors.New("invalid task timing projection")

// Instrumentation begins at this migration, not at reconstructed historic
// timestamps. Prior tasks deliberately have no retrospective timing samples.
func migrateTaskTiming(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE TABLE task_timing_metadata(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version=1),started_at TEXT NOT NULL);
 CREATE TABLE task_timings(
 task_id TEXT PRIMARY KEY REFERENCES task_heads(task_id),started_at TEXT,
 terminal_event_id TEXT,terminal_sequence INTEGER,state TEXT NOT NULL CHECK(state IN ('running','completed','failed','canceled')),
 duration_ns INTEGER,reason TEXT NOT NULL CHECK(reason IN ('pending','observed','missing_start','invalid_time')),
 CHECK((state='running' AND reason='pending' AND started_at IS NOT NULL AND terminal_event_id IS NULL AND terminal_sequence IS NULL AND duration_ns IS NULL)
 OR (state<>'running' AND terminal_event_id IS NOT NULL AND length(terminal_event_id)>0 AND terminal_sequence IS NOT NULL AND terminal_sequence>1 AND
 ((reason='observed' AND started_at IS NOT NULL AND duration_ns IS NOT NULL AND duration_ns>=0)
 OR(reason='missing_start' AND started_at IS NULL AND duration_ns IS NULL)
 OR(reason='invalid_time' AND started_at IS NOT NULL AND duration_ns IS NULL)))));
 INSERT INTO task_timing_metadata(singleton,version,started_at) VALUES(1,1,?);
 PRAGMA user_version=29;`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type taskTiming struct {
	started, terminal  sql.NullString
	sequence, duration sql.NullInt64
	state, reason      string
}

func (p taskTiming) validate() error {
	if p.started.Valid {
		at, err := time.Parse(time.RFC3339Nano, p.started.String)
		if err != nil || at.IsZero() || at.UTC().Format(time.RFC3339Nano) != p.started.String {
			return errTaskTiming
		}
	}
	if p.state == "running" {
		if p.reason != "pending" || !p.started.Valid || p.terminal.Valid || p.sequence.Valid || p.duration.Valid {
			return errTaskTiming
		}
		return nil
	}
	if p.state != "completed" && p.state != "failed" && p.state != "canceled" {
		return errTaskTiming
	}
	if !p.terminal.Valid || p.terminal.String == "" || !p.sequence.Valid || p.sequence.Int64 < 2 {
		return errTaskTiming
	}
	switch p.reason {
	case "observed":
		if !p.started.Valid || !p.duration.Valid || p.duration.Int64 < 0 {
			return errTaskTiming
		}
	case "missing_start":
		if p.started.Valid || p.duration.Valid {
			return errTaskTiming
		}
	case "invalid_time":
		if !p.started.Valid || p.duration.Valid {
			return errTaskTiming
		}
	default:
		return errTaskTiming
	}
	return nil
}

// appendTaskTiming is part of the caller's event/head transaction, including
// synthetic recovery terminals. It records elapsed event wall time, not model
// latency, active execution time, acceptance evidence or quality.
func appendTaskTiming(ctx context.Context, tx *sql.Tx, e runtime.Event) error {
	if e.Kind != runtime.TaskStarted && e.Kind != runtime.TaskCompleted && e.Kind != runtime.TaskFailed && e.Kind != runtime.TaskCanceled {
		return nil
	}
	if e.Validate() != nil {
		return errTaskTiming
	}
	var schema int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return err
	}
	if schema < 29 {
		return nil
	} // Legacy fixture/writer: instrumentation is unavailable.
	if schema > 30 {
		return errTaskTiming
	}
	var old taskTiming
	err := tx.QueryRowContext(ctx, `SELECT started_at,terminal_event_id,terminal_sequence,state,duration_ns,reason FROM task_timings WHERE task_id=?`, e.TaskID).Scan(&old.started, &old.terminal, &old.sequence, &old.state, &old.duration, &old.reason)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists && (old.validate() != nil || old.state != "running") {
		return errTaskTiming
	}
	if exists {
		var recorded sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN json_extract(body,'$.kind')='task.started' AND length(CAST(json_extract(body,'$.time') AS BLOB))<=64 THEN json_extract(body,'$.time') END FROM events WHERE task_id=? AND sequence=1`, e.TaskID).Scan(&recorded); err != nil || !recorded.Valid {
			return errTaskTiming
		}
		at, parseErr := time.Parse(time.RFC3339Nano, recorded.String)
		if parseErr != nil || at.UTC().Format(time.RFC3339Nano) != old.started.String {
			return errTaskTiming
		}
	}
	if e.Kind == runtime.TaskStarted {
		if exists || e.Sequence != 1 {
			return errTaskTiming
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_timings(task_id,started_at,state,reason) VALUES(?,?,'running','pending')`, e.TaskID, e.Time.UTC().Format(time.RFC3339Nano))
		return err
	}
	state := map[runtime.Kind]string{runtime.TaskCompleted: "completed", runtime.TaskFailed: "failed", runtime.TaskCanceled: "canceled"}[e.Kind]
	next := taskTiming{state: state, reason: "missing_start", terminal: sql.NullString{String: e.ID, Valid: true}, sequence: sql.NullInt64{Int64: e.Sequence, Valid: true}}
	if exists {
		next.started = old.started
		start, _ := time.Parse(time.RFC3339Nano, old.started.String)
		duration := e.Time.Sub(start)
		next.reason = "invalid_time"
		if duration >= 0 && start.Add(duration).Equal(e.Time) {
			next.reason = "observed"
			next.duration = sql.NullInt64{Int64: int64(duration), Valid: true}
		}
	}
	if next.validate() != nil {
		return errTaskTiming
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_timings(task_id,started_at,terminal_event_id,terminal_sequence,state,duration_ns,reason) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(task_id) DO UPDATE SET started_at=excluded.started_at,terminal_event_id=excluded.terminal_event_id,terminal_sequence=excluded.terminal_sequence,state=excluded.state,duration_ns=excluded.duration_ns,reason=excluded.reason`, e.TaskID, next.started, next.terminal, next.sequence, next.state, next.duration, next.reason)
	return err
}
