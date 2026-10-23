package chatfiles

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"strconv"
	"time"
)

// RemoteIndex is presentation metadata only. Execution authority always comes
// from the immutable request/route binding and a fresh remote observation.
type RemoteIndex struct {
	Sequence  int64     `json:"sequence"`
	Instance  string    `json:"instance"`
	RequestID string    `json:"request_id"`
	Root      string    `json:"root"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}
type ConversationSnapshot struct {
	Sequence int64
	Body     []byte
}

func (s *Store) ConversationSnapshots(ctx context.Context, after int64, limit int) ([]ConversationSnapshot, error) {
	if after < 0 || limit < 1 || limit > 50 {
		return nil, ErrFile
	}
	rows, e := s.db.QueryContext(ctx, `SELECT rowid,body FROM files WHERE rowid>? AND substr(chat,1,13)='conversation_' AND NOT EXISTS(SELECT 1 FROM remote_chat_index WHERE scope=chat) ORDER BY rowid LIMIT ?`, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ConversationSnapshot{}
	total := 0
	for rows.Next() {
		var v ConversationSnapshot
		if e = rows.Scan(&v.Sequence, &v.Body); e != nil {
			return nil, e
		}
		total += len(v.Body)
		if len(v.Body) > 2<<20 || total > 8<<20 {
			if len(out) == 0 {
				return nil, ErrFile
			}
			break
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) IndexConversation(ctx context.Context, scope string, v RemoteIndex) error {
	if !webui.ValidID(scope) || !webui.ValidID(v.Instance) || !webui.ValidID(v.RequestID) || !webui.ValidID(v.Root) || len(v.Title) > 512 || len(v.Model) > 512 {
		return ErrFile
	}
	body, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO remote_chat_index(scope,seq,instance,root,metadata) SELECT chat,rowid,?,?,? FROM files WHERE chat=? AND id=? ON CONFLICT(scope) DO NOTHING`, v.Instance, v.Root, body, scope, ID(scope, "history"))
	return e
}
func (s *Store) RemoteChats(ctx context.Context, after int64, limit int) ([]RemoteIndex, bool, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, false, ErrFile
	}
	rows, e := s.db.QueryContext(ctx, `SELECT seq,metadata FROM remote_chat_index WHERE seq IN (SELECT max(seq) FROM remote_chat_index GROUP BY instance,root) AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`, after, after, limit+1)
	if e != nil {
		return nil, false, e
	}
	defer rows.Close()
	out := []RemoteIndex{}
	for rows.Next() {
		var seq int64
		var body []byte
		var v RemoteIndex
		if rows.Scan(&seq, &body) != nil || json.Unmarshal(body, &v) != nil {
			return nil, false, ErrFile
		}
		v.Sequence = seq
		out = append(out, v)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}

// Observations are display-only cache. They cannot grant continuation/cancel
// authority. Terminal receipts are immutable and survive commander restarts.
func (s *Store) CacheRemoteObservation(ctx context.Context, scope string, body []byte) error {
	if !webui.ValidID(scope) || len(body) > 1<<20 {
		return ErrFile
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO remote_chat_observations(scope,body) VALUES(?,?) ON CONFLICT(scope) DO NOTHING`, scope, []byte{}); e != nil {
		return e
	}
	var old []byte
	if e = tx.QueryRowContext(ctx, `SELECT body FROM remote_chat_observations WHERE scope=?`, scope).Scan(&old); e != nil {
		return e
	}
	if len(old) > 0 {
		if string(old) != string(body) {
			return ErrFile
		}
		return tx.Commit()
	}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(body)),0) FROM remote_chat_observations`).Scan(&total); e != nil || total+int64(len(body)) > 128<<20 {
		return ErrFile
	}
	if _, e = tx.ExecContext(ctx, `UPDATE remote_chat_observations SET body=? WHERE scope=?`, body, scope); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) RemoteObservation(ctx context.Context, scope string) ([]byte, error) {
	if !webui.ValidID(scope) {
		return nil, ErrFile
	}
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT body FROM remote_chat_observations WHERE scope=?`, scope).Scan(&b)
	if len(b) > 1<<20 {
		return nil, ErrFile
	}
	return b, e
}
func RemoteCursor(v int64) string { return strconv.FormatInt(v, 10) }
