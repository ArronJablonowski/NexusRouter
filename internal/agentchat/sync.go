package agentchat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"time"
)

// Batch exports only locally produced messages; imported records never echo.
type Batch struct {
	Version  int                          `json:"version"`
	Stream   string                       `json:"stream"`
	After    int64                        `json:"after"`
	Next     int64                        `json:"next"`
	Messages []webui.CollaborationMessage `json:"messages"`
}

func (b Batch) Validate() error {
	if b.Version != 1 || len(b.Stream) != 32 || b.After < 0 || b.Next < b.After || b.Messages == nil || len(b.Messages) > 50 {
		return ErrInvalid
	}
	if _, e := hex.DecodeString(b.Stream); e != nil {
		return ErrInvalid
	}
	previous := b.After
	for _, m := range b.Messages {
		if m.Validate() != nil || m.OriginInstance != "" || m.Sequence <= previous {
			return ErrInvalid
		}
		previous = m.Sequence
	}
	if b.Next != previous {
		return ErrInvalid
	}
	return nil
}
func (s *Store) Export(ctx context.Context, stream string, after int64) (Batch, error) {
	b := Batch{Version: 1, After: after, Next: after, Messages: []webui.CollaborationMessage{}}
	if after < 0 {
		return b, ErrInvalid
	}
	if e := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE name='stream'").Scan(&b.Stream); e != nil {
		return b, e
	}
	if stream != "" && stream != b.Stream {
		return b, ErrConflict
	}
	var maximum int64
	if e := s.db.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM messages WHERE json_extract(body,'$.origin_instance') IS NULL`).Scan(&maximum); e != nil {
		return b, e
	}
	if after > maximum {
		return b, ErrConflict
	}
	rows, e := s.db.QueryContext(ctx, `SELECT body FROM messages WHERE seq>? AND json_extract(body,'$.origin_instance') IS NULL ORDER BY seq LIMIT 50`, after)
	if e != nil {
		return b, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var m webui.CollaborationMessage
		if e = rows.Scan(&raw); e != nil {
			return b, e
		}
		if json.Unmarshal([]byte(raw), &m) != nil {
			return b, ErrInvalid
		}
		b.Messages = append(b.Messages, m)
		b.Next = m.Sequence
	}
	if e = rows.Err(); e != nil {
		return b, e
	}
	return b, b.Validate()
}
func (s *Store) Cursor(ctx context.Context, peer string) (string, int64, error) {
	var stream string
	var after int64
	e := s.db.QueryRowContext(ctx, "SELECT stream,position FROM sync_state WHERE peer=?", peer).Scan(&stream, &after)
	if e == sql.ErrNoRows {
		return "", 0, nil
	}
	return stream, after, e
}
func (s *Store) Import(ctx context.Context, peer string, b Batch) error {
	if !validPeer(peer) || b.Validate() != nil {
		return ErrInvalid
	}
	stream, after, e := s.Cursor(ctx, peer)
	if e != nil {
		return e
	}
	if (stream != "" && stream != b.Stream) || b.After != after {
		return ErrConflict
	}
	for _, m := range b.Messages {
		m.OriginInstance = peer
		m.OriginStream = b.Stream
		m.OriginSequence = m.Sequence
		identity, _ := json.Marshal([]any{peer, b.Stream, m.Sequence})
		sum := sha256.Sum256(identity)
		if _, e = s.Append(ctx, hex.EncodeToString(sum[:]), m); e != nil {
			return e
		}
	}
	// Append acknowledgement precedes cursor advancement. A crash between them
	// reimports exact identities without repeating inference or losing messages.
	result, e := s.db.ExecContext(ctx, `INSERT INTO sync_state(peer,stream,position,state,checked_at) VALUES(?,?,?,'synced',?) ON CONFLICT(peer) DO UPDATE SET stream=excluded.stream,position=excluded.position,state='synced',checked_at=excluded.checked_at WHERE (sync_state.stream='' OR sync_state.stream=excluded.stream) AND sync_state.position=?`, peer, b.Stream, b.Next, time.Now().UTC().Format(time.RFC3339Nano), b.After)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func validPeer(peer string) bool {
	m := webui.CollaborationMessage{OriginInstance: peer, OriginStream: "00000000000000000000000000000000"}
	return peer != "" && len(peer) <= 128 && m.ValidOrigin()
}
func (s *Store) SyncFailure(ctx context.Context, peer string) error {
	if !validPeer(peer) {
		return ErrInvalid
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO sync_state(peer,stream,position,state,checked_at) VALUES(?,'',0,'unavailable',?) ON CONFLICT(peer) DO UPDATE SET state='unavailable',checked_at=excluded.checked_at`, peer, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (s *Store) SyncStatus(ctx context.Context) ([]webui.CollaborationSync, error) {
	out := []webui.CollaborationSync{}
	rows, e := s.db.QueryContext(ctx, "SELECT peer,state,checked_at FROM sync_state ORDER BY peer LIMIT 129")
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var item webui.CollaborationSync
		var at string
		if e = rows.Scan(&item.Instance, &item.State, &at); e != nil {
			return out, e
		}
		item.CheckedAt, e = time.Parse(time.RFC3339Nano, at)
		if e != nil {
			return out, e
		}
		out = append(out, item)
	}
	if len(out) > 128 {
		return nil, ErrLimit
	}
	return out, rows.Err()
}
func (s *Store) initSync(ctx context.Context) error {
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS metadata(name TEXT PRIMARY KEY,value TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS sync_state(peer TEXT PRIMARY KEY,stream TEXT NOT NULL,position INTEGER NOT NULL,state TEXT NOT NULL,checked_at TEXT NOT NULL)`} {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	raw := make([]byte, 16)
	if _, e := rand.Read(raw); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO metadata(name,value) VALUES('stream',?)", hex.EncodeToString(raw))
	return e
}

func (s *Store) SyncPending(ctx context.Context, peer string) error {
	_, e := s.db.ExecContext(ctx, "UPDATE sync_state SET state='catching_up' WHERE peer=?", peer)
	return e
}
