package remote

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

const maxAuditArchiveRows = 10000
const maxAuditArchiveBytes = 32 << 20

type auditArchive struct {
	Version  int          `json:"version"`
	Instance string       `json:"instance"`
	Through  int64        `json:"through"`
	Entries  []AuditEntry `json:"entries"`
}

type AuditArchiveReceipt struct {
	Instance string `json:"instance"`
	Through  int64  `json:"through"`
	Entries  int    `json:"entries"`
	SHA256   string `json:"sha256"`
}

type AuditPruneReceipt struct {
	AuditArchiveReceipt
	AlreadyApplied bool `json:"already_applied"`
}

// ArchiveAudit publishes a durable private archive without changing the journal.
// through=0 selects the oldest at most 10,000 current audit entries. An explicit
// through must be a present sequence and cannot select more than 10,000 entries.
// Existing files are accepted only if their exact bytes match this snapshot.
func ArchiveAudit(ctx context.Context, directory, instance, path string, through int64) (AuditArchiveReceipt, error) {
	var zero AuditArchiveReceipt
	if !id(instance) || through < 0 {
		return zero, ErrInvalid
	}
	db, err := openAuditDatabase(directory, false)
	if err != nil {
		return zero, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if err = checkAuditIdentity(ctx, tx, instance); err != nil {
		return zero, err
	}
	ceiling := through
	if ceiling == 0 {
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM audit").Scan(&ceiling); err != nil {
			return zero, err
		}
	}
	rows, err := readAuditRows(ctx, tx, instance, 0, ceiling, maxAuditArchiveRows+1)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrUnavailable
	}
	if through > 0 && (len(rows) > maxAuditArchiveRows || rows[len(rows)-1].Sequence != through) {
		return zero, ErrConflict
	}
	if len(rows) > maxAuditArchiveRows {
		rows = rows[:maxAuditArchiveRows]
	}
	archive := auditArchive{Version: 1, Instance: instance, Through: rows[len(rows)-1].Sequence, Entries: rows}
	body, err := json.Marshal(archive)
	if err != nil || len(body) > maxAuditArchiveBytes {
		return zero, ErrInvalid
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	if err = publishAuditArchive(path, body); err != nil {
		return zero, err
	}
	return archive.receipt(body), nil
}

// PruneAudit removes only the exact archived audit prefix in one transaction.
// Request identities, runtime evidence and learning ledgers are never touched.
// The archive is checked and synced before mutation; its externally supplied hash
// must match. A later audit marker records the archive hash atomically with delete.
func PruneAudit(ctx context.Context, directory, instance, path, expected string) (AuditPruneReceipt, error) {
	var zero AuditPruneReceipt
	if !id(instance) || len(expected) != 64 {
		return zero, ErrInvalid
	}
	body, err := readAuditArchive(path, true)
	if err != nil {
		return zero, err
	}
	if certificateDigest(body) != expected {
		return zero, ErrConflict
	}
	var archive auditArchive
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&archive) != nil {
		return zero, ErrInvalid
	}
	canonical, err := json.Marshal(archive)
	if err != nil || !bytes.Equal(canonical, body) || archive.Version != 1 || archive.Instance != instance || archive.Through <= 0 || len(archive.Entries) == 0 || len(archive.Entries) > maxAuditArchiveRows {
		return zero, ErrInvalid
	}
	if archive.Entries[len(archive.Entries)-1].Sequence != archive.Through {
		return zero, ErrInvalid
	}
	db, err := openAuditDatabase(directory, true)
	if err != nil {
		return zero, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if err = checkAuditIdentity(ctx, tx, instance); err != nil {
		return zero, err
	}
	rows, err := readAuditRows(ctx, tx, instance, 0, archive.Through, maxAuditArchiveRows+1)
	if err != nil {
		return zero, err
	}
	receipt := AuditPruneReceipt{AuditArchiveReceipt: archive.receipt(body)}
	if len(rows) == 0 {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM audit WHERE caller='local-admin' AND destination=? AND action='audit_prune' AND request_id=? AND outcome='completed' AND sequence>?", instance, expected, archive.Through).Scan(&count)
		if err != nil {
			return zero, err
		}
		if count != 1 {
			return zero, ErrConflict
		}
		receipt.AlreadyApplied = true
		return receipt, nil
	}
	if !reflect.DeepEqual(rows, archive.Entries) {
		return zero, ErrConflict
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM audit WHERE sequence<=?", archive.Through)
	if err != nil {
		return zero, err
	}
	removed, err := result.RowsAffected()
	if err != nil || removed != int64(len(rows)) {
		return zero, ErrConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit(at,caller,destination,action,request_id,outcome) VALUES(?,?,?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), "local-admin", instance, "audit_prune", expected, "completed")
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func checkAuditIdentity(ctx context.Context, tx *sql.Tx, instance string) error {
	var count, version int
	var owner string
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM identity").Scan(&count) != nil || count != 1 || tx.QueryRowContext(ctx, "SELECT version,CASE WHEN length(CAST(instance AS BLOB))<=256 THEN instance END FROM identity").Scan(&version, &owner) != nil || version != Version || owner != instance {
		return ErrConflict
	}
	return nil
}
func (a auditArchive) receipt(body []byte) AuditArchiveReceipt {
	return AuditArchiveReceipt{Instance: a.Instance, Through: a.Through, Entries: len(a.Entries), SHA256: certificateDigest(body)}
}
func archiveParent(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrInvalid
	}
	parent := filepath.Dir(path)
	st, err := os.Lstat(parent)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return "", ErrDenied
	}
	return parent, nil
}
func syncArchiveParent(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func readAuditArchive(path string, sync bool) ([]byte, error) {
	if _, err := archiveParent(path); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > maxAuditArchiveBytes {
		return nil, ErrDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, ErrDenied
	}
	body, err := io.ReadAll(io.LimitReader(f, maxAuditArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAuditArchiveBytes {
		return nil, ErrInvalid
	}
	if sync {
		if err = f.Sync(); err != nil {
			return nil, err
		}
		if err = syncArchiveParent(path); err != nil {
			return nil, err
		}
	}
	return body, nil
}
func publishAuditArchive(path string, body []byte) error {
	parent, err := archiveParent(path)
	if err != nil {
		return err
	}
	compare := func() error {
		saved, err := readAuditArchive(path, true)
		if err != nil {
			return err
		}
		if !bytes.Equal(saved, body) {
			return ErrConflict
		}
		return nil
	}
	if err = compare(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(parent, ".nexus-audit-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return compare()
}
