package telemetry

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/workers"
	"strings"
	"time"
)

var ErrLeaseBusy = errors.New("resource lease unavailable")
var ErrLeaseLost = errors.New("resource lease expired or released")
var ErrLeaseInput = errors.New("invalid resource lease request")

type Lease = workers.Lease

// AcquireLease serializes overlapping scopes across database connections.
// Scope IDs must be canonicalized by the application (not model-supplied paths).
// Reserved workspace and legacy create_* scopes mutually overlap so upgrading
// builtin file scopes never abandons an unreleased old holder or nested root.
// Other scopes retain exact identity semantics.
// Unreleased writers block readers and writers; unreleased readers block
// writers even after expiry. Only a joined holder may explicitly release its
// lease. Expiry alone cannot fence an in-process handler or prove it stopped.
// Read holders must also stop using results after lease loss.
func (s *Store) AcquireLease(ctx context.Context, task, owner, scope string, writer bool, now time.Time, ttl time.Duration) (Lease, error) {
	l := Lease{}
	if task == "" || owner == "" || scope == "" || len(scope) > 512 || !leaseTime(now, ttl) {
		return l, ErrLeaseInput
	}
	process, err := processguard.Current(ctx)
	if err != nil {
		return l, ErrLeaseLost
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return l, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE task_heads SET sequence=sequence WHERE task_id=?", task); err != nil {
		return l, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM task_heads WHERE task_id=?", task).Scan(&state); err != nil {
		return l, err
	}
	if state != "running" {
		return l, ErrLeaseLost
	}
	var conflicts int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM resource_leases WHERE
	(scope=? OR (?=1 AND (scope='workspace' OR scope GLOB 'create_*')))
	AND released=0 AND (writer=1 OR ?=1)`, scope, filesystemLeaseScope(scope), writer).Scan(&conflicts)
	if err != nil {
		return l, err
	}
	if conflicts > 0 {
		return l, ErrLeaseBusy
	}
	if err := registerLeaseProcess(ctx, tx, process); err != nil {
		return l, err
	}
	l = Lease{Token: rand.Text(), TaskID: task, Owner: owner, Scope: scope, Writer: writer, Expires: now.Add(ttl).UTC()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_leases(token,task_id,owner,scope,writer,expires,process_id) VALUES(?,?,?,?,?,?,?)`, l.Token, task, owner, scope, writer, l.Expires.UnixNano(), process.ID); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		return Lease{}, err
	}
	return l, nil
}

// RenewLease is a heartbeat from the current holder. Lost leases never revive.
func (s *Store) RenewLease(ctx context.Context, token, owner string, now time.Time, ttl time.Duration) error {
	if token == "" || owner == "" || !leaseTime(now, ttl) {
		return ErrLeaseInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE resource_leases SET owner=owner WHERE token=? AND owner=?`, token, owner); err != nil {
		return err
	}
	if err := leaseProcessGate(ctx, tx, token, owner); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE resource_leases SET expires=max(expires,?) WHERE token=? AND owner=? AND released=0 AND expires>? AND task_id IN (SELECT task_id FROM task_heads WHERE state='running')`, now.Add(ttl).UnixNano(), token, owner, now.UnixNano())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return tx.Commit()
}

// ReleaseLease asserts the holder has actually stopped all use of the scope.
// Never call this just because a context was canceled or a heartbeat expired.
// The opaque token is a capability; it must not be exposed in public telemetry.
func (s *Store) ReleaseLease(ctx context.Context, token, owner string) error {
	if token == "" || owner == "" {
		return ErrLeaseInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE resource_leases SET owner=owner WHERE token=? AND owner=?`, token, owner); err != nil {
		return err
	}
	if err := leaseProcessGate(ctx, tx, token, owner); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE resource_leases SET released=1 WHERE token=? AND owner=?", token, owner)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return tx.Commit()
}

// InspectLeases is for trusted supervision, not public output: it includes
// holder tokens. It returns unreleased overlapping scopes, including reserved
// workspace/create_* compatibility aliases; Lease.Scope retains the actual
// stored identity. Observation never changes exact authorization bindings.
// State can be re-derived after restart without private maps.
func (s *Store) InspectLeases(ctx context.Context, scope string) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token,task_id,owner,scope,writer,expires,released FROM resource_leases
	WHERE (scope=? OR (?=1 AND (scope='workspace' OR scope GLOB 'create_*')))
	AND released=0 ORDER BY expires,token LIMIT 1000`, scope, filesystemLeaseScope(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := []Lease{}
	for rows.Next() {
		var l Lease
		var expires int64
		if err := rows.Scan(&l.Token, &l.TaskID, &l.Owner, &l.Scope, &l.Writer, &expires, &l.Released); err != nil {
			return nil, err
		}
		l.Expires = time.Unix(0, expires).UTC()
		leases = append(leases, l)
	}
	return leases, rows.Err()
}
func leaseTime(now time.Time, ttl time.Duration) bool {
	return now.UTC().Year() >= 1970 && now.UTC().Year() < 2261 && ttl >= time.Millisecond && ttl <= 10*time.Minute
}

func filesystemLeaseScope(scope string) bool {
	return scope == "workspace" || strings.HasPrefix(scope, "create_")
}
