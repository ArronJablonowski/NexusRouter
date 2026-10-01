package remote

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Journal owns a separate versioned control-plane database, never the runtime
// telemetry schema. It stores hashes/ownership/audit, not prompts or results.
type Journal struct {
	db       *sql.DB
	instance string
}

func OpenJournal(directory, instance string) (*Journal, error) {
	if !id(instance) || !filepath.IsAbs(directory) {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(directory)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, ErrDenied
	}
	path := filepath.Join(directory, "remote.sqlite")
	created := false
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		created = true
		err = f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	st, err = os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrDenied
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Journal, error) { db.Close(); return nil, e }
	if created {
		tx, e := db.Begin()
		if e != nil {
			return fail(e)
		}
		_, e = tx.Exec(`CREATE TABLE identity(version INTEGER NOT NULL, instance TEXT NOT NULL);
CREATE TABLE requests(caller TEXT NOT NULL, request_id TEXT NOT NULL, digest TEXT NOT NULL, submission TEXT NOT NULL DEFAULT '', PRIMARY KEY(caller,request_id));
CREATE TABLE audit(sequence INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, caller TEXT NOT NULL, destination TEXT NOT NULL, action TEXT NOT NULL, request_id TEXT NOT NULL, outcome TEXT NOT NULL);`)
		if e == nil {
			_, e = tx.Exec("INSERT INTO identity VALUES(1,?)", instance)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return fail(e)
		}
	}
	var version int
	var owner string
	var count int
	if db.QueryRow("SELECT count(*) FROM identity").Scan(&count) != nil || count != 1 || db.QueryRow("SELECT version,instance FROM identity").Scan(&version, &owner) != nil || version != Version || owner != instance {
		return fail(ErrConflict)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); err != nil {
		return fail(err)
	}
	return &Journal{db: db, instance: instance}, nil
}
func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) audit(ctx context.Context, caller, action, key, outcome string) error {
	_, err := j.db.ExecContext(ctx, "INSERT INTO audit(at,caller,destination,action,request_id,outcome) VALUES(?,?,?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), caller, j.instance, action, key, outcome)
	return err
}
func (j *Journal) reserve(ctx context.Context, caller, key, digest string) (string, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO requests(caller,request_id,digest) VALUES(?,?,?) ON CONFLICT DO NOTHING", caller, key, digest); err != nil {
		return "", err
	}
	var old, submission string
	if err = tx.QueryRowContext(ctx, "SELECT digest,submission FROM requests WHERE caller=? AND request_id=?", caller, key).Scan(&old, &submission); err != nil {
		return "", err
	}
	if old != digest {
		return "", ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return submission, nil
}
func (j *Journal) bind(ctx context.Context, caller, key, submission string) error {
	result, err := j.db.ExecContext(ctx, "UPDATE requests SET submission=? WHERE caller=? AND request_id=? AND (submission='' OR submission=?)", submission, caller, key, submission)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrConflict
	}
	return nil
}
func (j *Journal) lookup(ctx context.Context, caller, key string) (string, error) {
	var submission string
	err := j.db.QueryRowContext(ctx, "SELECT submission FROM requests WHERE caller=? AND request_id=?", caller, key).Scan(&submission)
	if err != nil || submission == "" {
		return "", ErrUnavailable
	}
	return submission, nil
}
