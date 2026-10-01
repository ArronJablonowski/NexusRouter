package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// EvidenceStore persists trusted host evidence, not arbitrary harness claims.
// It is separate from runtime telemetry and never modifies the runtime schema.
// Callers must authenticate provenance/reviewers before using these methods.
type EvidenceStore struct{ db *sql.DB }

const maxEvidenceBytes = 32768

func OpenEvidenceStore(directory string) (*EvidenceStore, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	if e := os.MkdirAll(directory, 0700); e != nil {
		return nil, e
	}
	st, e := os.Lstat(directory)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	path := filepath.Join(directory, "harness-evidence.sqlite")
	file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	created := e == nil
	if created {
		if e = file.Close(); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	st, e = os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	u := url.URL{Scheme: "file", Path: path}
	db, e := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*EvidenceStore, error) { db.Close(); return nil, e }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if created {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE ledger_identity(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL,kind TEXT NOT NULL);
INSERT INTO ledger_identity VALUES(1,1,'nexus-harness-evidence');
CREATE TABLE executions(id TEXT PRIMARY KEY,digest TEXT NOT NULL UNIQUE,body BLOB NOT NULL);
CREATE TABLE reviews(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,execution_digest TEXT NOT NULL REFERENCES executions(digest),body BLOB NOT NULL);
CREATE INDEX reviews_by_execution ON reviews(execution_digest,sequence);`)
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return fail(e)
		}
	}
	var version, count int
	var kind string
	if db.QueryRowContext(ctx, "SELECT count(*) FROM ledger_identity").Scan(&count) != nil || count != 1 || db.QueryRowContext(ctx, "SELECT version,kind FROM ledger_identity WHERE singleton=1").Scan(&version, &kind) != nil || version != Version || kind != "nexus-harness-evidence" {
		return fail(ErrInvalid)
	}
	if _, e = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); e != nil {
		return fail(e)
	}
	dir, e := os.Open(directory)
	if e != nil {
		return fail(e)
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return fail(e)
	}
	return &EvidenceStore{db}, nil
}
func (s *EvidenceStore) Close() error {
	if s == nil || s.db == nil {
		return ErrInvalid
	}
	return s.db.Close()
}
func (s *EvidenceStore) write(ctx context.Context) (*sql.Tx, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	// Obtain SQLite's write lock before reading the current head, including
	// across processes/independent handles. A stale read cannot race a revision.
	_, e = tx.ExecContext(ctx, "UPDATE ledger_identity SET version=version WHERE singleton=1")
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func encodeEvidence(v any) ([]byte, error) {
	body, e := json.Marshal(v)
	if e != nil || len(body) > maxEvidenceBytes {
		return nil, ErrInvalid
	}
	return body, nil
}

// AppendExecution is immutable and idempotent. It never creates a quality vote.
func (s *EvidenceStore) AppendExecution(ctx context.Context, execution Execution, now time.Time) error {
	if execution.Validate() != nil || !validTime(now) || execution.CompletedAt.After(now) {
		return ErrInvalid
	}
	body, e := encodeEvidence(execution)
	if e != nil {
		return e
	}
	digest, _ := execution.Digest()
	tx, e := s.write(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var old string
	var saved []byte
	e = tx.QueryRowContext(ctx, "SELECT digest,CASE WHEN length(body)<=32768 THEN body END FROM executions WHERE id=?", execution.ID).Scan(&old, &saved)
	if e == nil {
		if old != digest || string(saved) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM executions").Scan(&count); e != nil {
		return e
	}
	if count >= MaxRecords {
		return ErrInvalid
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO executions(id,digest,body) VALUES(?,?,?)", execution.ID, digest, body); e != nil {
		return e
	}
	return tx.Commit()
}

// AppendReview atomically checks the exact current head. Retrying an identical
// saved review is a no-op even after a later revision; conflicting IDs reject.
func (s *EvidenceStore) AppendReview(ctx context.Context, review Review, now time.Time) error {
	if review.Validate() != nil || !validTime(now) || review.CreatedAt.After(now) {
		return ErrInvalid
	}
	body, e := encodeEvidence(review)
	if e != nil {
		return e
	}
	tx, e := s.write(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var old []byte
	e = tx.QueryRowContext(ctx, "SELECT CASE WHEN length(body)<=32768 THEN body END FROM reviews WHERE id=?", review.ID).Scan(&old)
	if e == nil {
		if string(old) != string(body) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var execution Execution
	if e = tx.QueryRowContext(ctx, "SELECT CASE WHEN length(body)<=32768 THEN body END FROM executions WHERE digest=?", review.ExecutionDigest).Scan(&old); errors.Is(e, sql.ErrNoRows) {
		return ErrConflict
	} else if e != nil {
		return e
	}
	if json.Unmarshal(old, &execution) != nil || execution.Validate() != nil || hash(execution) != review.ExecutionDigest || execution.Status != "completed" || review.CreatedAt.Before(execution.CompletedAt) {
		return ErrInvalid
	}
	var headID string
	e = tx.QueryRowContext(ctx, "SELECT id,CASE WHEN length(body)<=32768 THEN body END FROM reviews WHERE execution_digest=? ORDER BY sequence DESC LIMIT 1", review.ExecutionDigest).Scan(&headID, &old)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var headTime time.Time
	if e == nil {
		var head Review
		if json.Unmarshal(old, &head) != nil || head.Validate() != nil || head.ID != headID || head.ExecutionDigest != review.ExecutionDigest {
			return ErrInvalid
		}
		headTime = head.CreatedAt
	}
	if review.ExpectedHead != headID {
		return ErrConflict
	}
	if !headTime.IsZero() && review.CreatedAt.Before(headTime) {
		return ErrInvalid
	}
	if review.Verdict == "withdrawn" && headID == "" {
		return ErrInvalid
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM reviews").Scan(&count); e != nil {
		return e
	}
	if count >= MaxRecords {
		return ErrInvalid
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO reviews(id,execution_digest,body) VALUES(?,?,?)", review.ID, review.ExecutionDigest, body); e != nil {
		return e
	}
	return tx.Commit()
}

// Snapshot reads one consistent transaction and validates the complete log with
// the same replay rules as the ranker. Corrupt or oversized records fail closed.
func (s *EvidenceStore) Snapshot(ctx context.Context, now time.Time) (*Snapshot, error) {
	if s == nil || s.db == nil || ctx == nil || !validTime(now) {
		return nil, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	executions, e := readExecutions(ctx, tx)
	if e != nil {
		return nil, e
	}
	reviews, e := readReviews(ctx, tx)
	if e != nil {
		return nil, e
	}
	snapshot, e := Replay(executions, reviews, now)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return snapshot, nil
}
func readExecutions(ctx context.Context, tx *sql.Tx) ([]Execution, error) {
	rows, e := tx.QueryContext(ctx, "SELECT id,digest,CASE WHEN length(body)<=32768 THEN body END FROM executions ORDER BY id LIMIT 100001")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var id, digest string
		var body []byte
		var execution Execution
		if rows.Scan(&id, &digest, &body) != nil || json.Unmarshal(body, &execution) != nil || execution.ID != id || hash(execution) != digest || len(out) >= MaxRecords {
			return nil, ErrInvalid
		}
		out = append(out, execution)
	}
	return out, rows.Err()
}
func readReviews(ctx context.Context, tx *sql.Tx) ([]Review, error) {
	rows, e := tx.QueryContext(ctx, "SELECT id,execution_digest,CASE WHEN length(body)<=32768 THEN body END FROM reviews ORDER BY sequence LIMIT 100001")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var id, digest string
		var body []byte
		var review Review
		if rows.Scan(&id, &digest, &body) != nil || json.Unmarshal(body, &review) != nil || review.ID != id || review.ExecutionDigest != digest || len(out) >= MaxRecords {
			return nil, ErrInvalid
		}
		out = append(out, review)
	}
	return out, rows.Err()
}
