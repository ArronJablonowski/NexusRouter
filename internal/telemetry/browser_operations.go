package telemetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	MaxBrowserOperationRequestBytes  = 1 << 20
	MaxBrowserOperationResponseBytes = 64 << 10
	MaxBrowserOperations             = 10_000
	BrowserOperationRetainedCount    = 5_000
	BrowserOperationRetention        = 30 * 24 * time.Hour
)

var (
	ErrBrowserOperationConflict = errors.New("browser operation conflict")
	ErrBrowserOperationInvalid  = errors.New("invalid browser operation")
	ErrBrowserOperationCapacity = errors.New("browser operation capacity exhausted")
)

type BrowserOperation struct {
	Version       int
	OperationID   string
	Subject       string
	Kind          string
	RequestDigest string
	State         string
	Response      []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (s *Store) BeginBrowserOperation(ctx context.Context, subject, key, kind string, request []byte) (BrowserOperation, error) {
	if s == nil || ctx == nil || !validBrowserDigest(subject) || !validBrowserKey(key) || !validBrowserKind(kind) || len(request) == 0 || len(request) > MaxBrowserOperationRequestBytes {
		return BrowserOperation{}, ErrBrowserOperationInvalid
	}
	keyHash := sha256.Sum256([]byte(subject + "\x00" + key))
	requestHash := sha256.Sum256(request)
	keyDigest, requestDigest := hex.EncodeToString(keyHash[:]), hex.EncodeToString(requestHash[:])
	operationID := "op_" + keyDigest
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserOperation{}, err
	}
	defer tx.Rollback()
	if record, readErr := readBrowserOperation(ctx, tx, operationID); readErr == nil {
		if record.Subject != subject || record.Kind != kind || record.RequestDigest != requestDigest {
			return BrowserOperation{}, ErrBrowserOperationConflict
		}
		if err = tx.Commit(); err != nil {
			return BrowserOperation{}, err
		}
		return record, nil
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return BrowserOperation{}, readErr
	}
	now := time.Now().UTC()
	if err = pruneBrowserOperations(ctx, tx, now); err != nil {
		return BrowserOperation{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM browser_operations").Scan(&count); err != nil {
		return BrowserOperation{}, err
	}
	if count >= MaxBrowserOperations {
		return BrowserOperation{}, ErrBrowserOperationCapacity
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_operations(operation_id,session_subject,key_digest,kind,request_digest,state,response,created_at,updated_at)
	 VALUES(?,?,?,?,?,'pending',NULL,?,?)`, operationID, subject, keyDigest, kind, requestDigest, now.UnixNano(), now.UnixNano())
	if err != nil {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	if err = tx.Commit(); err != nil {
		return BrowserOperation{}, err
	}
	return BrowserOperation{Version: 1, OperationID: operationID, Subject: subject, Kind: kind, RequestDigest: requestDigest, State: "pending", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) CommitBrowserOperation(ctx context.Context, subject, operationID, requestDigest string, response []byte) (BrowserOperation, error) {
	return s.finishBrowserOperation(ctx, subject, operationID, requestDigest, "committed", response)
}

func (s *Store) RejectBrowserOperation(ctx context.Context, subject, operationID, requestDigest string, response []byte) (BrowserOperation, error) {
	return s.finishBrowserOperation(ctx, subject, operationID, requestDigest, "rejected", response)
}

func (s *Store) finishBrowserOperation(ctx context.Context, subject, operationID, requestDigest, state string, response []byte) (BrowserOperation, error) {
	if s == nil || ctx == nil || !validBrowserDigest(subject) || !validBrowserOperationID(operationID) || !validBrowserDigest(requestDigest) || len(response) == 0 || len(response) > MaxBrowserOperationResponseBytes {
		return BrowserOperation{}, ErrBrowserOperationInvalid
	}
	if state != "committed" && state != "rejected" {
		return BrowserOperation{}, ErrBrowserOperationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserOperation{}, err
	}
	defer tx.Rollback()
	record, err := readBrowserOperation(ctx, tx, operationID)
	if err != nil || record.Subject != subject || record.RequestDigest != requestDigest {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	if record.State != "pending" {
		if record.State != state || string(record.Response) != string(response) {
			return BrowserOperation{}, ErrBrowserOperationConflict
		}
		return record, tx.Commit()
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE browser_operations SET state=?,response=?,updated_at=?
	 WHERE operation_id=? AND session_subject=? AND state='pending' AND request_digest=?`, state, response, now.UnixNano(), operationID, subject, requestDigest)
	if err != nil {
		return BrowserOperation{}, err
	}
	if changed, changeErr := result.RowsAffected(); changeErr != nil || changed != 1 {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	if err = tx.Commit(); err != nil {
		return BrowserOperation{}, err
	}
	record.State, record.Response, record.UpdatedAt = state, append([]byte(nil), response...), now
	return record, nil
}

func (s *Store) BrowserOperations(ctx context.Context, subject, after string, limit int) ([]BrowserOperation, string, error) {
	if s == nil || ctx == nil || !validBrowserDigest(subject) || limit < 1 || limit > 100 || after != "" && !validBrowserOperationID(after) {
		return nil, "", ErrBrowserOperationInvalid
	}
	args := []any{subject}
	query := `SELECT operation_id,session_subject,kind,request_digest,state,response,created_at,updated_at FROM browser_operations WHERE session_subject=?`
	if after != "" {
		var created int64
		if err := s.db.QueryRowContext(ctx, "SELECT created_at FROM browser_operations WHERE operation_id=? AND session_subject=?", after, subject).Scan(&created); err != nil {
			return nil, "", ErrBrowserOperationConflict
		}
		query += " AND (created_at<? OR (created_at=? AND operation_id<?))"
		args = append(args, created, created, after)
	}
	query += " ORDER BY created_at DESC,operation_id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]BrowserOperation, 0, limit+1)
	for rows.Next() {
		record, scanErr := scanBrowserOperation(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		items = append(items, record)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		next = items[limit-1].OperationID
		items = items[:limit]
	}
	return items, next, nil
}

func pruneBrowserOperations(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM browser_operations WHERE state IN('committed','rejected') AND updated_at<?", now.Add(-BrowserOperationRetention).UnixNano()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM browser_operations WHERE operation_id IN (
	 SELECT operation_id FROM browser_operations WHERE state IN('committed','rejected') ORDER BY updated_at DESC,operation_id DESC LIMIT -1 OFFSET ?)`, BrowserOperationRetainedCount)
	return err
}

type browserOperationScanner interface{ Scan(...any) error }

func readBrowserOperation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (BrowserOperation, error) {
	return scanBrowserOperation(q.QueryRowContext(ctx, `SELECT operation_id,session_subject,kind,request_digest,state,response,created_at,updated_at FROM browser_operations WHERE operation_id=?`, id))
}

func scanBrowserOperation(scanner browserOperationScanner) (BrowserOperation, error) {
	var record BrowserOperation
	var created, updated int64
	if err := scanner.Scan(&record.OperationID, &record.Subject, &record.Kind, &record.RequestDigest, &record.State, &record.Response, &created, &updated); err != nil {
		return BrowserOperation{}, err
	}
	record.Version = 1
	record.CreatedAt, record.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	if !record.Valid() {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	return record, nil
}

func (r BrowserOperation) Valid() bool {
	return r.Version == 1 && validBrowserOperationID(r.OperationID) && validBrowserDigest(r.Subject) && validBrowserKind(r.Kind) && validBrowserDigest(r.RequestDigest) &&
		(r.State == "pending" && len(r.Response) == 0 || (r.State == "committed" || r.State == "rejected") && len(r.Response) > 0 && len(r.Response) <= MaxBrowserOperationResponseBytes) && !r.CreatedAt.IsZero() && !r.UpdatedAt.Before(r.CreatedAt)
}

func validBrowserKey(v string) bool {
	return len(v) >= 16 && len(v) <= 128 && strings.TrimSpace(v) == v
}
func validBrowserKind(v string) bool { return len(v) >= 3 && len(v) <= 64 && strings.TrimSpace(v) == v }
func validBrowserDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func validBrowserOperationID(v string) bool {
	return strings.HasPrefix(v, "op_") && validBrowserDigest(strings.TrimPrefix(v, "op_"))
}
