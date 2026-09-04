package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"darwinrouter/memory"
)

var _ memory.Store = (*Store)(nil)

// PutMemory creates at expected=0 or corrects with optimistic concurrency.
// Corrections retain creation/last-use and cannot silently weaken privacy.
// Provenance may change to identify the correction's source; creation remains
// the original fact's timestamp while Updated identifies the correction time.
// No previous factual payload is retained: deletion must not leave revision
// copies queryable. Physical WAL/backups erasure requires operator retention.
func (s *Store) PutMemory(ctx context.Context, f memory.Fact, expected int64) error {
	if f.Validate() != nil || expected < 0 || f.Revision-1 != expected {
		return memory.ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE memory_facts SET revision=revision WHERE scope=? AND id=?", f.Scope, f.ID); err != nil {
		return err
	}
	var body []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM memory_facts WHERE scope=? AND id=?", f.Scope, f.ID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		if expected != 0 || !f.Created.Equal(f.Updated) || !f.LastUse.IsZero() {
			return memory.ErrConflict
		}
	} else if err != nil {
		return err
	} else {
		var old memory.Fact
		if json.Unmarshal(body, &old) != nil || old.Validate() != nil {
			return memory.ErrInput
		}
		if old.Revision != expected || !old.Created.Equal(f.Created) || !old.LastUse.Equal(f.LastUse) || f.Updated.Before(old.Updated) || (old.Privacy == "local_only" && f.Privacy != old.Privacy) {
			return memory.ErrConflict
		}
	}
	f.Created, f.Updated = f.Created.UTC(), f.Updated.UTC()
	f.LastUse, f.Expires = f.LastUse.UTC(), f.Expires.UTC()
	body, err = json.Marshal(f)
	if err != nil {
		return err
	}
	expires := int64(0)
	if !f.Expires.IsZero() {
		expires = f.Expires.UnixNano()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_facts(scope,id,revision,privacy,expires,content,body) VALUES(?,?,?,?,?,?,?)
	 ON CONFLICT(scope,id) DO UPDATE SET revision=excluded.revision,privacy=excluded.privacy,expires=excluded.expires,content=excluded.content,body=excluded.body`, f.Scope, f.ID, f.Revision, f.Privacy, expires, f.Content, body)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// QueryMemory does not touch last-use; inspection/export is read-only. Contains
// is a literal, case-sensitive substring (not SQL LIKE or a relevance model).
func (s *Store) QueryMemory(ctx context.Context, q memory.Query) ([]memory.Fact, error) {
	if q.Validate() != nil {
		return nil, memory.ErrInput
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM memory_facts WHERE scope=? AND id>? AND
	 (? OR privacy='shareable') AND (? OR expires=0 OR expires>?) AND instr(content,?)>0 ORDER BY id LIMIT ?`, q.Scope, q.AfterID, q.LocalOnly, q.IncludeExpired, q.Now.UnixNano(), q.Contains, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memory.Fact{}
	for rows.Next() {
		var body []byte
		var f memory.Fact
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if json.Unmarshal(body, &f) != nil || f.Validate() != nil || f.Scope != q.Scope || (!q.LocalOnly && f.Privacy != "shareable") || (!q.IncludeExpired && !f.Expires.IsZero() && !f.Expires.After(q.Now)) {
			return nil, memory.ErrInput
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) TouchMemory(ctx context.Context, scope, id string, now time.Time) error {
	if !memory.ValidKey(scope) || !memory.ValidKey(id) || !memory.ValidTime(now) {
		return memory.ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE memory_facts SET revision=revision WHERE scope=? AND id=?", scope, id); err != nil {
		return err
	}
	var body []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM memory_facts WHERE scope=? AND id=?", scope, id).Scan(&body); errors.Is(err, sql.ErrNoRows) {
		return memory.ErrConflict
	} else if err != nil {
		return err
	}
	var f memory.Fact
	if json.Unmarshal(body, &f) != nil || f.Validate() != nil {
		return memory.ErrInput
	}
	if now.Before(f.Created) || (!f.Expires.IsZero() && !f.Expires.After(now)) {
		return memory.ErrConflict
	}
	if now.After(f.LastUse) {
		f.LastUse = now.UTC()
	}
	body, err = json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE memory_facts SET body=? WHERE scope=? AND id=?", body, scope, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteMemory(ctx context.Context, scope, id string, expected int64) error {
	if !memory.ValidKey(scope) || !memory.ValidKey(id) || expected < 1 {
		return memory.ErrInput
	}
	r, err := s.db.ExecContext(ctx, "DELETE FROM memory_facts WHERE scope=? AND id=? AND revision=?", scope, id, expected)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return memory.ErrConflict
	}
	return nil
}

func (s *Store) ExpireMemory(ctx context.Context, scope string, now time.Time) (int64, error) {
	if !memory.ValidKey(scope) || !memory.ValidTime(now) {
		return 0, memory.ErrInput
	}
	r, err := s.db.ExecContext(ctx, "DELETE FROM memory_facts WHERE scope=? AND expires>0 AND expires<=?", scope, now.UnixNano())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
