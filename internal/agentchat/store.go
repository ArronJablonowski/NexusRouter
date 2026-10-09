// Package agentchat owns the private, append-only model collaboration journal.
package agentchat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/webui"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

var ErrInvalid = errors.New("invalid collaboration message")
var ErrConflict = errors.New("collaboration message identity conflict")
var ErrLimit = errors.New("collaboration storage or task limit reached")

type Store struct{ db *sql.DB }

func Open(ctx context.Context, directory string) (*Store, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	st, err := os.Lstat(directory)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	path := filepath.Join(directory, "messages.db")
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, e := os.Lstat(name)
		if e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, ErrInvalid
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	values := url.Values{}
	for _, p := range []string{"busy_timeout(5000)", "synchronous(FULL)"} {
		values.Add("_pragma", p)
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	for _, q := range []string{"PRAGMA journal_mode=WAL", `CREATE TABLE IF NOT EXISTS messages (seq INTEGER PRIMARY KEY AUTOINCREMENT, call_key TEXT UNIQUE NOT NULL, task TEXT NOT NULL, recipient TEXT NOT NULL, topic TEXT NOT NULL, private INTEGER NOT NULL, body TEXT NOT NULL)`, "CREATE INDEX IF NOT EXISTS messages_task ON messages(task)", "CREATE INDEX IF NOT EXISTS messages_inbox ON messages(recipient,seq)"} {
		if _, err = db.ExecContext(ctx, q); err != nil {
			db.Close()
			return nil, err
		}
	}
	var mode string
	var synchronous int
	if db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" || db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous) != nil || synchronous != 2 {
		db.Close()
		return nil, ErrInvalid
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, e := os.Lstat(name)
		if e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			db.Close()
			return nil, ErrInvalid
		}
	}
	dir, err := os.Open(directory)
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

// Append uses host-issued call identity. Exact repetition returns the original
// timestamp and sequence; changed content never overwrites a previous message.
func (s *Store) Append(ctx context.Context, key string, m webui.CollaborationMessage) (webui.CollaborationMessage, error) {
	m.Sequence = 1
	m.SentAt = time.Unix(1, 0).UTC()
	if len(key) != 64 || m.Validate() != nil {
		return webui.CollaborationMessage{}, ErrInvalid
	}
	canonical, _ := json.Marshal(m)
	if len(canonical)+64 > 9<<10 {
		return webui.CollaborationMessage{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer lock before checking quotas/idempotency.
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET seq=seq WHERE seq=-1"); err != nil {
		return m, err
	}
	var body string
	var seq int64
	err = tx.QueryRowContext(ctx, "SELECT seq,body FROM messages WHERE call_key=?", key).Scan(&seq, &body)
	if err == nil {
		var old webui.CollaborationMessage
		if json.Unmarshal([]byte(body), &old) != nil || old.Validate() != nil {
			return m, ErrInvalid
		}
		copy := old
		copy.Sequence = m.Sequence
		copy.SentAt = m.SentAt
		encoded, _ := json.Marshal(copy)
		if string(encoded) != string(canonical) {
			return m, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	var count, total, taskCount int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(CAST(body AS BLOB))),0),coalesce(sum(task=?),0) FROM messages", m.TaskID).Scan(&count, &total, &taskCount); err != nil {
		return m, err
	}
	if count >= 50000 || total+int64(len(canonical))+64 > 256<<20 || taskCount >= 64 {
		return m, ErrLimit
	}
	m.SentAt = time.Now().UTC()
	bodyBytes, _ := json.Marshal(m)
	result, err := tx.ExecContext(ctx, "INSERT INTO messages(call_key,task,recipient,topic,private,body) VALUES(?,?,?,?,?,?)", key, m.TaskID, m.Recipient, m.Topic, m.Private, string(bodyBytes))
	if err != nil {
		return m, err
	}
	seq, err = result.LastInsertId()
	if err != nil {
		return m, err
	}
	m.Sequence = seq
	bodyBytes, _ = json.Marshal(m)
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE seq=?", string(bodyBytes), seq); err != nil {
		return m, err
	}
	if err = tx.Commit(); err != nil {
		return m, err
	}
	return m, nil
}

// Read returns newest first, bounded to 50. Empty recipient is operator-only
// history; model inboxes include only their own addressed and shared messages.
func (s *Store) Read(ctx context.Context, o webui.CollaborationOptions, recipient string, local bool) (webui.CollaborationPage, error) {
	out := webui.CollaborationPage{Version: 1, Enabled: true, Messages: []webui.CollaborationMessage{}}
	if o.Validate() != nil {
		return out, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM messages WHERE (?=0 OR seq<?) AND (?='' OR topic=?) AND (?='' OR recipient=? OR recipient='*') AND (? OR private=0) ORDER BY seq DESC LIMIT 51`, o.Before, o.Before, o.Topic, o.Topic, recipient, recipient, local)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var m webui.CollaborationMessage
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if json.Unmarshal([]byte(raw), &m) != nil || m.Validate() != nil {
			return out, ErrInvalid
		}
		if len(out.Messages) == 50 {
			out.NextBefore = out.Messages[49].Sequence
			break
		}
		out.Messages = append(out.Messages, m)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, out.Validate()
}
