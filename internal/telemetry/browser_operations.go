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
	Legacy        bool
}

type BrowserOperationRecovery struct {
	OperationID     string
	RecoverySubject string
	RecoveredAt     time.Time
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
	if err = reserveBrowserOperationWrite(ctx, tx); err != nil {
		return BrowserOperation{}, err
	}
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
		// Terminal rows normally follow the retention policy, but under hard-cap
		// pressure they are evictable so reconciled crashes do not consume a
		// permanent slot while pending rows remain deliberately non-prunable.
		if _, err = tx.ExecContext(ctx, `DELETE FROM browser_operations WHERE operation_id IN (
		 SELECT operation_id FROM browser_operations WHERE state IN('committed','rejected')
		 ORDER BY updated_at,operation_id LIMIT ?)`, count-MaxBrowserOperations+1); err != nil {
			return BrowserOperation{}, err
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM browser_operations").Scan(&count); err != nil {
			return BrowserOperation{}, err
		}
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

// AdoptableBrowserOperation finds an exact pending request initiated by a
// different browser session. The key is re-derived against each initiating
// subject, so a matching body digest alone can never authorize adoption.
// Multiple matches fail closed rather than selecting an ambiguous owner.
func (s *Store) AdoptableBrowserOperation(ctx context.Context, recoverySubject, key, kind string, request []byte) (BrowserOperation, bool, error) {
	if s == nil || ctx == nil || !validBrowserDigest(recoverySubject) || !validBrowserKey(key) || !validBrowserKind(kind) || len(request) == 0 || len(request) > MaxBrowserOperationRequestBytes {
		return BrowserOperation{}, false, ErrBrowserOperationInvalid
	}
	requestHash := sha256.Sum256(request)
	requestDigest := hex.EncodeToString(requestHash[:])
	rows, err := s.db.QueryContext(ctx, `SELECT o.operation_id,o.session_subject,o.key_digest,o.kind,o.request_digest,o.state,o.response,o.created_at,o.updated_at,
	 EXISTS(SELECT 1 FROM legacy_browser_workboard_operations l WHERE l.operation_id=o.operation_id)
	 FROM browser_operations o WHERE o.state='pending' AND o.kind=? AND o.request_digest=? AND o.session_subject<>?
	 ORDER BY o.created_at,o.operation_id`, kind, requestDigest, recoverySubject)
	if err != nil {
		return BrowserOperation{}, false, err
	}
	defer rows.Close()
	var candidate BrowserOperation
	found := false
	for rows.Next() {
		var record BrowserOperation
		var keyDigest string
		var created, updated int64
		if err = rows.Scan(&record.OperationID, &record.Subject, &keyDigest, &record.Kind, &record.RequestDigest, &record.State, &record.Response, &created, &updated, &record.Legacy); err != nil {
			return BrowserOperation{}, false, err
		}
		wantKey := sha256.Sum256([]byte(record.Subject + "\x00" + key))
		if keyDigest != hex.EncodeToString(wantKey[:]) || record.OperationID != "op_"+keyDigest {
			continue
		}
		record.Version = 1
		record.CreatedAt, record.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
		if !record.Valid() {
			return BrowserOperation{}, false, ErrBrowserOperationConflict
		}
		if found {
			return BrowserOperation{}, false, ErrBrowserOperationConflict
		}
		candidate, found = record, true
	}
	if err = rows.Err(); err != nil {
		return BrowserOperation{}, false, err
	}
	return candidate, found, nil
}

// RecoverBrowserOperation terminalizes an adopted row without changing its
// initiating session subject. A separate immutable row attributes the browser
// session that reconciled the durable domain outcome.
func (s *Store) RecoverBrowserOperation(ctx context.Context, recoverySubject string, record BrowserOperation, state string, response []byte) (BrowserOperation, error) {
	if s == nil || ctx == nil || !validBrowserDigest(recoverySubject) || recoverySubject == record.Subject || !record.Valid() || record.State != "pending" ||
		(state != "committed" && state != "rejected") || len(response) == 0 || len(response) > MaxBrowserOperationResponseBytes {
		return BrowserOperation{}, ErrBrowserOperationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserOperation{}, err
	}
	defer tx.Rollback()
	if err = reserveBrowserOperationWrite(ctx, tx); err != nil {
		return BrowserOperation{}, err
	}
	current, err := readBrowserOperation(ctx, tx, record.OperationID)
	if err != nil || current.Subject != record.Subject || current.Kind != record.Kind || current.RequestDigest != record.RequestDigest {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	now := time.Now().UTC()
	if current.State == "pending" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE browser_operations SET state=?,response=?,updated_at=?
		 WHERE operation_id=? AND session_subject=? AND state='pending' AND request_digest=?`, state, response, now.UnixNano(), record.OperationID, record.Subject, record.RequestDigest)
		if updateErr != nil {
			return BrowserOperation{}, updateErr
		}
		if changed, changeErr := result.RowsAffected(); changeErr != nil || changed != 1 {
			return BrowserOperation{}, ErrBrowserOperationConflict
		}
		current.State, current.Response, current.UpdatedAt = state, append([]byte(nil), response...), now
	} else if current.State != state || string(current.Response) != string(response) {
		return BrowserOperation{}, ErrBrowserOperationConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO browser_operation_recoveries(operation_id,recovery_subject,recovered_at) VALUES(?,?,?)`, record.OperationID, recoverySubject, now.UnixNano())
	if err != nil {
		return BrowserOperation{}, err
	}
	if changed, changeErr := result.RowsAffected(); changeErr != nil {
		return BrowserOperation{}, changeErr
	} else if changed == 0 {
		var existing string
		if err = tx.QueryRowContext(ctx, `SELECT recovery_subject FROM browser_operation_recoveries WHERE operation_id=?`, record.OperationID).Scan(&existing); err != nil || existing != recoverySubject {
			return BrowserOperation{}, ErrBrowserOperationConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return BrowserOperation{}, err
	}
	return current, nil
}

func (s *Store) BrowserOperationRecovery(ctx context.Context, operationID string) (BrowserOperationRecovery, error) {
	if s == nil || ctx == nil || !validBrowserOperationID(operationID) {
		return BrowserOperationRecovery{}, ErrBrowserOperationInvalid
	}
	var recovery BrowserOperationRecovery
	var recovered int64
	err := s.db.QueryRowContext(ctx, `SELECT operation_id,recovery_subject,recovered_at FROM browser_operation_recoveries WHERE operation_id=?`, operationID).
		Scan(&recovery.OperationID, &recovery.RecoverySubject, &recovered)
	if err != nil {
		return BrowserOperationRecovery{}, err
	}
	recovery.RecoveredAt = time.Unix(0, recovered).UTC()
	if recovery.OperationID != operationID || !validBrowserDigest(recovery.RecoverySubject) || recovery.RecoveredAt.IsZero() {
		return BrowserOperationRecovery{}, ErrBrowserOperationConflict
	}
	return recovery, nil
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
	if err = reserveBrowserOperationWrite(ctx, tx); err != nil {
		return BrowserOperation{}, err
	}
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

// reserveBrowserOperationWrite acquires SQLite's writer reservation before a
// receipt lookup establishes a WAL snapshot. An unrelated runtime write between
// SELECT and UPDATE would otherwise cause SQLITE_BUSY_SNAPSHOT, which the busy
// timeout cannot repair. This no-op changes no operation or approval state.
func reserveBrowserOperationWrite(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE browser_operations SET state=state WHERE 0`)
	return err
}
