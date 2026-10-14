// Package chatfiles retains immutable, chat-scoped generated output snapshots.
package chatfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/webui"
	_ "modernc.org/sqlite"
)

var ErrFile = errors.New("chat file unavailable")

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dir string) (*Store, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, ErrFile
	}
	if e := os.Mkdir(dir, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, ErrFile
	}
	p := filepath.Join(dir, "files.db")
	for _, n := range []string{p, p + "-wal", p + "-shm"} {
		if i, e := os.Lstat(n); e == nil {
			if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
				return nil, ErrFile
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA journal_mode=WAL", `CREATE TABLE IF NOT EXISTS files(chat TEXT NOT NULL,id TEXT NOT NULL,metadata BLOB NOT NULL,body BLOB NOT NULL,PRIMARY KEY(chat,id))`} {
		if _, e = db.ExecContext(ctx, stmt); e != nil {
			db.Close()
			return nil, e
		}
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func ID(chat, source string) string {
	v := sha256.Sum256([]byte(chat + "\x00" + source))
	return "file_" + hex.EncodeToString(v[:20])
}
func (s *Store) Put(ctx context.Context, chat string, f webui.ChatFile, b []byte) error {
	if !webui.ValidID(chat) || f.Validate() != nil || f.Size != len(b) {
		return ErrFile
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// A write first serializes quota checks across independent readers/processes.
	if _, e = tx.ExecContext(ctx, `INSERT INTO files(chat,id,metadata,body) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM files WHERE chat=? AND id=?)`, chat, f.ID, []byte(`{}`), []byte{}, chat, f.ID); e != nil {
		return e
	}
	var oldMeta, oldBody []byte
	if e = tx.QueryRowContext(ctx, `SELECT metadata,body FROM files WHERE chat=? AND id=?`, chat, f.ID).Scan(&oldMeta, &oldBody); e != nil {
		return e
	}
	meta, _ := json.Marshal(f)
	if !bytes.Equal(oldMeta, []byte(`{}`)) {
		if !bytes.Equal(meta, oldMeta) || !bytes.Equal(b, oldBody) {
			return ErrFile
		}
		return tx.Commit()
	}
	var count, total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(body)),0) FROM files WHERE chat=?`, chat).Scan(&count, &total); e != nil || count > 100 || total+len(b) > 256<<20 {
		return ErrFile
	}
	var global int
	if e = tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(body)),0) FROM files`).Scan(&global); e != nil || global+len(b) > 512<<20 {
		return ErrFile
	}
	if _, e = tx.ExecContext(ctx, `UPDATE files SET metadata=?,body=? WHERE chat=? AND id=?`, meta, b, chat, f.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) List(ctx context.Context, chat string) ([]webui.ChatFile, error) {
	if !webui.ValidID(chat) {
		return nil, ErrFile
	}
	rows, e := s.db.QueryContext(ctx, `SELECT metadata FROM files WHERE chat=? ORDER BY rowid LIMIT 101`, chat)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []webui.ChatFile{}
	for rows.Next() {
		var b []byte
		var f webui.ChatFile
		if rows.Scan(&b) != nil || json.Unmarshal(b, &f) != nil || f.Validate() != nil || len(out) >= 100 {
			return nil, ErrFile
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, chat, id string) (webui.ChatFile, []byte, error) {
	var f webui.ChatFile
	var m, b []byte
	if !webui.ValidID(chat) || !webui.ValidID(id) {
		return f, nil, ErrFile
	}
	if e := s.db.QueryRowContext(ctx, `SELECT metadata,body FROM files WHERE chat=? AND id=?`, chat, id).Scan(&m, &b); e != nil {
		return f, nil, ErrFile
	}
	if json.Unmarshal(m, &f) != nil || f.Validate() != nil || f.Size != len(b) {
		return f, nil, ErrFile
	}
	return f, b, nil
}
