package remote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// LogStore is a private replica, never the commander's runtime or learning DB.
// Records and cursors commit together. It does not interpret remote text as
// instructions, synthesize quality feedback, or import remote authorization.
type LogStore struct{ db *sql.DB }

func OpenLogStore(directory string) (*LogStore, error) {
	dir, err := OpenRouteStore(directory)
	if err != nil {
		return nil, err
	}
	unlock, err := lockReviewQueue(dir.directory)
	if err != nil {
		return nil, err
	}
	defer unlock()
	path := filepath.Join(directory, "logs.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = file.Close()
	} else if errors.Is(err, os.ErrExist) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrDenied
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		side, e := os.Lstat(path + suffix)
		if e == nil && (!side.Mode().IsRegular() || side.Mode().Perm()&0077 != 0) {
			return nil, ErrDenied
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=rw&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
 CREATE TABLE IF NOT EXISTS log_identity(version INTEGER PRIMARY KEY CHECK(version=1));
 INSERT OR IGNORE INTO log_identity VALUES(1);
 CREATE TABLE IF NOT EXISTS log_heads(source TEXT NOT NULL,stream TEXT NOT NULL,cursor TEXT NOT NULL DEFAULT '',last_success TEXT NOT NULL DEFAULT '',last_attempt TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',PRIMARY KEY(source,stream));
 CREATE TABLE IF NOT EXISTS log_records(source TEXT NOT NULL,stream TEXT NOT NULL,position INTEGER NOT NULL,record_id TEXT NOT NULL,event_at TEXT NOT NULL,kind TEXT NOT NULL,task_id TEXT NOT NULL,session_id TEXT NOT NULL,body BLOB NOT NULL,sha256 TEXT NOT NULL,collected_at TEXT NOT NULL,PRIMARY KEY(source,stream,position));
 CREATE INDEX IF NOT EXISTS log_tasks ON log_records(source,task_id,stream,position);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &LogStore{db}, nil
}
func (s *LogStore) Close() error { return s.db.Close() }

type LogStatus struct {
	Source      string `json:"source"`
	Stream      string `json:"stream"`
	Records     int64  `json:"records"`
	LastSuccess string `json:"last_success"`
	LastAttempt string `json:"last_attempt"`
	Error       string `json:"error,omitempty"`
}

func (s *LogStore) Status(ctx context.Context) ([]LogStatus, error) {
	if s == nil || ctx == nil {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, `SELECT h.source,h.stream,(SELECT count(*) FROM log_records r WHERE r.source=h.source AND r.stream=h.stream),h.last_success,h.last_attempt,h.error FROM log_heads h ORDER BY h.source,h.stream`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []LogStatus{}
	for rows.Next() {
		var r LogStatus
		if e = rows.Scan(&r.Source, &r.Stream, &r.Records, &r.LastSuccess, &r.LastAttempt, &r.Error); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *LogStore) cursor(ctx context.Context, source, stream string) (string, error) {
	if !id(source) || !logStream(stream) {
		return "", ErrInvalid
	}
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT cursor FROM log_heads WHERE source=? AND stream=?", source, stream).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// CollectLogs pulls one bounded page, authenticating the paired peer anew.
func (c *Client) CollectLogs(ctx context.Context, s *LogStore, source, stream string) error {
	if c == nil || s == nil || ctx == nil {
		return ErrInvalid
	}
	after, err := s.cursor(ctx, source, stream)
	if err != nil {
		return err
	}
	page, err := c.Logs(ctx, source, stream, after)
	if err == nil {
		err = s.append(ctx, page)
	}
	if err != nil {
		label := "unavailable"
		if errors.Is(err, ErrDenied) {
			label = "denied"
		}
		if errors.Is(err, ErrConflict) {
			label = "cursor_or_history_conflict"
		}
		if errors.Is(err, ErrRateLimited) {
			label = "rate_limited"
		}
		// No transport error text or remote content enters operational diagnostics.
		_, saveErr := s.db.ExecContext(ctx, `INSERT INTO log_heads(source,stream,last_attempt,error) VALUES(?,?,?,?) ON CONFLICT(source,stream) DO UPDATE SET last_attempt=excluded.last_attempt,error=excluded.error`, source, stream, time.Now().UTC().Format(time.RFC3339Nano), label)
		if saveErr != nil {
			return saveErr
		}
	}
	return err
}
func (s *LogStore) append(ctx context.Context, p LogPage) error {
	if err := p.validate(p.Instance, p.Stream, p.After); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO log_heads(source,stream) VALUES(?,?)", p.Instance, p.Stream)
	if err != nil {
		return err
	}
	var old string
	if err = tx.QueryRowContext(ctx, "SELECT cursor FROM log_heads WHERE source=? AND stream=?", p.Instance, p.Stream).Scan(&old); err != nil {
		return err
	}
	if old != p.After {
		return ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, r := range p.Records {
		if _, err = tx.ExecContext(ctx, `INSERT INTO log_records VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.Instance, p.Stream, r.Position, r.ID, r.At.UTC().Format(time.RFC3339Nano), r.Kind, r.TaskID, r.SessionID, []byte(r.Body), certificateDigest(r.Body), now); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE log_heads SET cursor=?,last_success=?,last_attempt=?,error='' WHERE source=? AND stream=?", p.Next, now, now, p.Instance, p.Stream); err != nil {
		return err
	}
	return tx.Commit()
}

type CollectedLog struct {
	Source string `json:"source"`
	Stream string `json:"stream"`
	LogRecord
	SHA256      string `json:"sha256"`
	CollectedAt string `json:"collected_at"`
}

// Read explicitly returns private content, bounded by record count and bytes.
// Position is the pagination cursor; task filtering never renumbers positions.
func (s *LogStore) Read(ctx context.Context, source, stream, task string, after int64, limit int) ([]CollectedLog, error) {
	if s == nil || ctx == nil {
		return nil, ErrInvalid
	}
	if !id(source) || !logStream(stream) || (task != "" && !name(task)) || after < 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT position,record_id,event_at,kind,task_id,session_id,body,sha256,collected_at FROM log_records WHERE source=? AND stream=? AND position>? AND (?='' OR task_id=?) ORDER BY position LIMIT ?`, source, stream, after, task, task, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CollectedLog{}
	size := 0
	for rows.Next() {
		r := CollectedLog{Source: source, Stream: stream}
		var at string
		if err = rows.Scan(&r.Position, &r.ID, &at, &r.Kind, &r.TaskID, &r.SessionID, &r.Body, &r.SHA256, &r.CollectedAt); err != nil {
			return nil, err
		}
		r.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil || certificateDigest(r.Body) != r.SHA256 {
			return nil, ErrConflict
		}
		b, e := json.Marshal(r)
		if e != nil {
			return nil, e
		}
		if size+len(b) > maxLogPageBytes {
			break
		}
		size += len(b)
		out = append(out, r)
	}
	return out, rows.Err()
}
