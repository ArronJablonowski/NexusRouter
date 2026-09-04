// Package telemetry owns transactional local persistence.
package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"darwinrouter/runtime"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("event sequence or identity conflict")

type Store struct{ db *sql.DB }

// Open creates a private on-disk database. Callers must use a dedicated data
// directory; application configuration must never point into shared scratch.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("on-disk database path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var mode, integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("WAL mode unavailable")
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("database integrity check failed")
	}
	// BEGIN IMMEDIATE serializes migration discovery and application across processes.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var version int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 4 {
		return errors.New("unsupported database version")
	}
	if version == 0 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE task_heads (
		 task_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, sequence INTEGER NOT NULL, state TEXT NOT NULL);
		 CREATE TABLE events (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id),
		 sequence INTEGER NOT NULL CHECK(sequence > 0), body BLOB NOT NULL, UNIQUE(task_id, sequence));
		 PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if version < 2 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE evaluations (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), attempt_id TEXT NOT NULL,
		 model TEXT NOT NULL, provider TEXT NOT NULL, domain TEXT NOT NULL, profile TEXT NOT NULL,
		 body BLOB NOT NULL, UNIQUE(task_id,attempt_id));
		 CREATE TABLE fitness (model TEXT NOT NULL, provider TEXT NOT NULL, domain TEXT NOT NULL, profile TEXT NOT NULL,
		 samples INTEGER NOT NULL, quality REAL NOT NULL, compliance REAL NOT NULL, schema_samples INTEGER NOT NULL,
		 reliability REAL NOT NULL, latency REAL NOT NULL, cost REAL NOT NULL, updated INTEGER NOT NULL,
		 PRIMARY KEY(model,provider,domain,profile)); PRAGMA user_version=2;`)
		if err != nil {
			return err
		}
	}
	if version < 3 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE resource_leases (
		 token TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_heads(task_id), owner TEXT NOT NULL,
		 scope TEXT NOT NULL, writer INTEGER NOT NULL CHECK(writer IN (0,1)),
		 expires INTEGER NOT NULL, released INTEGER NOT NULL DEFAULT 0 CHECK(released IN (0,1)));
		 CREATE INDEX resource_leases_scope ON resource_leases(scope,released,expires);
		 PRAGMA user_version=3;`)
		if err != nil {
			return err
		}
	}
	if version < 4 {
		_, err = conn.ExecContext(ctx, `CREATE TABLE memory_facts (
		 scope TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
		 privacy TEXT NOT NULL, expires INTEGER NOT NULL, content TEXT NOT NULL, body BLOB NOT NULL,
		 PRIMARY KEY(scope,id));
		 CREATE INDEX memory_expiry ON memory_facts(scope,expires);
		 PRAGMA user_version=4;`)
		if err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Append commits the immutable event and task projection together. Repeating
// exactly the same event is safe after acknowledgement loss; reuse is rejected.
func (s *Store) Append(ctx context.Context, expected int64, e runtime.Event) error {
	body, err := e.Encode()
	if err != nil {
		return err
	}
	if expected < 0 || e.Sequence != expected+1 {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A write first acquires SQLite's writer reservation before reading state.
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", e.TaskID); err != nil {
		return err
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM events WHERE id=?", e.ID).Scan(&previous)
	if err == nil {
		if string(previous) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var seq int64
	var session, state string
	err = tx.QueryRowContext(ctx, "SELECT sequence, session_id, state FROM task_heads WHERE task_id=?", e.TaskID).Scan(&seq, &session, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if expected != 0 || e.Kind != runtime.TaskStarted {
			return ErrConflict
		}
		session, state = e.SessionID, "running"
		if _, err = tx.ExecContext(ctx, "INSERT INTO task_heads VALUES (?, ?, 0, ?)", e.TaskID, session, state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if seq != expected || session != e.SessionID || state != "running" || (seq > 0 && e.Kind == runtime.TaskStarted) {
		return ErrConflict
	}
	switch e.Kind {
	case runtime.TaskCompleted:
		state = "completed"
	case runtime.TaskFailed:
		state = "failed"
	case runtime.TaskCanceled:
		state = "canceled"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO events VALUES (?, ?, ?, ?)", e.ID, e.TaskID, e.Sequence, body); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=?, state=? WHERE task_id=?", e.Sequence, state, e.TaskID); err != nil {
		return err
	}
	return tx.Commit()
}

// Read returns bounded pages; after is an exclusive per-task sequence cursor.
func (s *Store) Read(ctx context.Context, task string, after int64, limit int) ([]runtime.Event, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid event page")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM events WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?", task, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]runtime.Event, 0)
	for rows.Next() {
		var body []byte
		var e runtime.Event
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &e); err != nil {
			return nil, err
		}
		if err = e.Validate(); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
