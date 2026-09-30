// Package hostresources provides a durable per-host resource coordinator.
package hostresources

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/resources"
	_ "modernc.org/sqlite"
)

const (
	MaxClockRollback = 5 * time.Second
	environmentPath  = "DARWIN_RESOURCE_COORDINATOR_DB"
)

var (
	ErrClockRollback = errors.New("host resource coordinator clock rollback")
	ErrRecovery      = errors.New("host resource reservation recovery unavailable")
)

type Coordinator struct {
	db       *sql.DB
	limits   resources.Limits
	adaptive bool
}

func Path() (string, error) {
	if p := branding.Getenv(environmentPath); p != "" {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return "", resources.ErrReservation
		}
		return p, nil
	}
	d, err := os.UserConfigDir()
	if err != nil || d == "" || !filepath.IsAbs(d) {
		return "", resources.ErrReservation
	}
	return filepath.Join(d, "DarwinRouter", "host-resources", "resource-coordinator.db"), nil
}
func Open(ctx context.Context, limits resources.Limits) (*Coordinator, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return openPath(ctx, p, limits, false)
}

// OpenAdaptive derives each admission's effective concurrency tier from the
// same durable aggregate and host snapshot used for fixed-limit admission.
func OpenAdaptive(ctx context.Context, limits resources.Limits) (*Coordinator, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return openPath(ctx, p, limits, true)
}

func OpenPath(ctx context.Context, path string, limits resources.Limits) (*Coordinator, error) {
	return openPath(ctx, path, limits, false)
}

func OpenPathAdaptive(ctx context.Context, path string, limits resources.Limits) (*Coordinator, error) {
	return openPath(ctx, path, limits, true)
}

func openPath(ctx context.Context, path string, limits resources.Limits, adaptive bool) (*Coordinator, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, resources.ErrReservation
	}
	if _, err := resources.NewBudget(limits); err != nil {
		return nil, resources.ErrReservation
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("resource coordinator directory is not private")
	}
	if existing, statErr := os.Lstat(path); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode().Perm()&0077 != 0 {
			return nil, errors.New("resource coordinator database is not private")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	fi, se := f.Stat()
	ce := f.Close()
	if se != nil || ce != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("resource coordinator database is not private")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Coordinator{db: db, limits: limits, adaptive: adaptive}
	if err = c.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}
func (c *Coordinator) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}
func (c *Coordinator) initialize(ctx context.Context) error {
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if err := initializePragma(ctx, c.db, q); err != nil {
			return err
		}
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var v int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v == 0 {
		if err = migrateV1(ctx, conn); err != nil {
			return err
		}
	} else if v != SchemaVersion {
		return errors.New("unsupported resource coordinator schema")
	}
	if err = validateSchema(ctx, conn); err != nil {
		return err
	}
	if err = bindPolicy(ctx, conn, c.limits, c.adaptive); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func bindPolicy(ctx context.Context, conn *sql.Conn, limits resources.Limits, adaptive bool) error {
	wantAdaptive := 0
	if adaptive {
		wantAdaptive = 1
	}
	want := []int64{int64(limits.MaxConcurrent), int64(math.Float64bits(limits.RAMPercent)), int64(math.Float64bits(limits.VRAMPercent)), int64(limits.MaxAge), int64(wantAdaptive)}
	var got [5]int64
	err := conn.QueryRowContext(ctx, "SELECT max_concurrent,ram_percent_bits,vram_percent_bits,max_age_ns,adaptive FROM coordinator_policy WHERE singleton=1").Scan(&got[0], &got[1], &got[2], &got[3], &got[4])
	if errors.Is(err, sql.ErrNoRows) {
		_, err = conn.ExecContext(ctx, "INSERT INTO coordinator_policy VALUES(1,?,?,?,?,?)", want[0], want[1], want[2], want[3], want[4])
		return err
	}
	if err != nil {
		return err
	}
	for index := range want {
		if got[index] != want[index] {
			return resources.ErrReservationConflict
		}
	}
	return nil
}

func (c *Coordinator) validatePolicy(ctx context.Context, conn *sql.Conn) error {
	return bindPolicy(ctx, conn, c.limits, c.adaptive)
}

type immediate struct {
	conn *sql.Conn
	done bool
}

func (c *Coordinator) begin(ctx context.Context) (*immediate, error) {
	if c == nil || c.db == nil || ctx == nil {
		return nil, resources.ErrReservation
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err = conn.ExecContext(ctx, q); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, err
	}
	return &immediate{conn: conn}, nil
}
func (t *immediate) rollback() {
	if t == nil {
		return
	}
	if !t.done {
		_, _ = t.conn.ExecContext(context.Background(), "ROLLBACK")
	}
	_ = t.conn.Close()
	t.done = true
}
func (t *immediate) commit(ctx context.Context) error {
	_, err := t.conn.ExecContext(ctx, "COMMIT")
	if err == nil {
		t.done = true
	}
	_ = t.conn.Close()
	return err
}
func clock(ctx context.Context, conn *sql.Conn, now time.Time) (time.Time, error) {
	if now.IsZero() || now.Year() < 1970 || now.Year() >= 2261 {
		return time.Time{}, resources.ErrReservation
	}
	now = now.UTC()
	var last int64
	if err := conn.QueryRowContext(ctx, "SELECT last_seen_ns FROM coordinator_clock WHERE singleton=1").Scan(&last); err != nil {
		return time.Time{}, err
	}
	if now.UnixNano() < last-MaxClockRollback.Nanoseconds() {
		return time.Time{}, ErrClockRollback
	}
	if now.UnixNano() < last {
		now = time.Unix(0, last).UTC()
	}
	_, err := conn.ExecContext(ctx, "UPDATE coordinator_clock SET last_seen_ns=? WHERE singleton=1", now.UnixNano())
	return now, err
}
func exactOwner(ctx context.Context, want resources.ReservationOwner) (processguard.Reference, error) {
	if want.Validate() != nil {
		return processguard.Reference{}, resources.ErrReservation
	}
	ref, err := processguard.Current(ctx)
	if err != nil || ref.Validate() != nil || ref.ID != want.ProcessID {
		return processguard.Reference{}, resources.ErrReservationOwner
	}
	return ref, nil
}
func registerProcess(ctx context.Context, conn *sql.Conn, ref processguard.Reference) error {
	body, err := json.Marshal(ref)
	if err != nil || len(body) > 8192 {
		return resources.ErrReservationOwner
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO owner_processes(id,body) VALUES(?,?) ON CONFLICT(id) DO NOTHING", ref.ID, body); err != nil {
		return err
	}
	var stored []byte
	if err = conn.QueryRowContext(ctx, "SELECT body FROM owner_processes WHERE id=?", ref.ID).Scan(&stored); err != nil || !bytes.Equal(body, stored) {
		return resources.ErrReservationOwner
	}
	return nil
}

func (c *Coordinator) Acquire(ctx context.Context, snapshot resources.Snapshot, request resources.ReservationRequest, now time.Time) (resources.ReservationBinding, error) {
	canonical, err := request.Canonical()
	if err != nil {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	request = canonical
	if request.RAMBytes > math.MaxInt64 || request.VRAMBytes > math.MaxInt64 {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	ref, err := exactOwner(ctx, request.Owner)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	digest, err := request.CanonicalDigest()
	if err != nil {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	if err = c.validatePolicy(ctx, tx.conn); err != nil {
		return resources.ReservationBinding{}, err
	}
	if d := now.Sub(request.RequestedAt); d < -MaxClockRollback || d > MaxClockRollback {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	if row, readErr := readRow(ctx, tx.conn, request.ReservationID); readErr == nil {
		if row.digest != digest {
			return resources.ReservationBinding{}, resources.ErrReservationConflict
		}
		if row.state != "active" || !now.Before(row.expires) {
			return row.binding(), resources.ErrReservationExpired
		}
		if err = tx.commit(ctx); err != nil {
			return resources.ReservationBinding{}, err
		}
		return row.binding(), nil
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return resources.ReservationBinding{}, readErr
	}
	if err = c.admit(ctx, tx.conn, snapshot, request, now); err != nil {
		return resources.ReservationBinding{}, err
	}
	if err = registerProcess(ctx, tx.conn, ref); err != nil {
		return resources.ReservationBinding{}, err
	}
	binding := resources.ReservationBinding{Version: resources.ReservationContractVersion, Request: request, RequestDigest: digest, AcquiredAt: now, ExpiresAt: now.Add(request.TTL).UTC()}
	if binding.Validate() != nil {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	body, _ := json.Marshal(request)
	_, err = tx.conn.ExecContext(ctx, `INSERT INTO reservations(id,request_digest,token,revision,state,process_id,daemon_id,host_scope,task_id,session_id,provider_id,model_id,profile,device,ram_bytes,vram_bytes,context_tokens,config_digest,acquired_at_ns,last_renewed_at_ns,expires_at_ns,request) VALUES(?,?,?,1,'active',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.ReservationID, digest, rand.Text(), ref.ID, request.Owner.DaemonID, request.HostScope, request.TaskID, request.SessionID, request.ProviderID, request.ModelID, request.Profile, request.GPUDevice, request.RAMBytes, request.VRAMBytes, request.ContextTokens, request.ConfigDigest, now.UnixNano(), now.UnixNano(), binding.ExpiresAt.UnixNano(), body)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	if _, err = event(ctx, tx.conn, request.ReservationID, 1, "acquired", "", now, ref.ID); err != nil {
		return resources.ReservationBinding{}, err
	}
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationBinding{}, err
	}
	return binding, nil
}
func (c *Coordinator) admit(ctx context.Context, conn *sql.Conn, s resources.Snapshot, r resources.ReservationRequest, now time.Time) error {
	var (
		b   *resources.Budget
		err error
	)
	if c.adaptive {
		b, err = resources.NewAdaptiveBudget(c.limits)
	} else {
		b, err = resources.NewBudget(c.limits)
	}
	if err != nil {
		return resources.ErrReservation
	}
	rows, err := conn.QueryContext(ctx, "SELECT id FROM reservations ORDER BY id")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		old, readErr := readRow(ctx, conn, id)
		if readErr != nil {
			return resources.ErrReservation
		}
		if old.request.HostScope != r.HostScope || old.state != "active" || !now.Before(old.expires) {
			continue
		}
		if _, err = b.Reserve(s, resources.Need{RAM: old.request.RAMBytes, VRAM: old.request.VRAMBytes, Device: old.request.GPUDevice}, now); err != nil {
			return resources.ErrCapacity
		}
	}
	_, err = b.Reserve(s, resources.Need{RAM: r.RAMBytes, VRAM: r.VRAMBytes, Device: r.GPUDevice}, now)
	return err
}

func (c *Coordinator) Renew(ctx context.Context, id string, owner resources.ReservationOwner, now time.Time, ttl time.Duration) (resources.ReservationBinding, error) {
	ref, err := exactOwner(ctx, owner)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	if ttl < time.Second || ttl > 10*time.Minute || ttl%time.Millisecond != 0 {
		return resources.ReservationBinding{}, resources.ErrReservation
	}
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	row, err := readRow(ctx, tx.conn, id)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	if row.processID != ref.ID || row.request.Owner != owner {
		return resources.ReservationBinding{}, resources.ErrReservationOwner
	}
	if row.state != "active" || !now.Before(row.expires) {
		return row.binding(), resources.ErrReservationExpired
	}
	next := now.Add(ttl).UTC()
	if row.expires.After(next) {
		next = row.expires
	}
	rev := row.revision + 1
	result, err := tx.conn.ExecContext(ctx, "UPDATE reservations SET revision=?,last_renewed_at_ns=?,expires_at_ns=? WHERE id=? AND revision=? AND state='active'", rev, now.UnixNano(), next.UnixNano(), id, row.revision)
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return resources.ReservationBinding{}, resources.ErrReservationConflict
	}
	if _, err = event(ctx, tx.conn, id, rev, "renewed", row.eventDigest, now, ref.ID); err != nil {
		return resources.ReservationBinding{}, err
	}
	row.revision, row.renewed, row.expires = rev, now, next
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationBinding{}, err
	}
	return row.binding(), nil
}
func (c *Coordinator) Release(ctx context.Context, id string, owner resources.ReservationOwner, now time.Time) (resources.ReservationStatus, error) {
	ref, err := exactOwner(ctx, owner)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	row, err := readRow(ctx, tx.conn, id)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	if row.processID != ref.ID || row.request.Owner != owner {
		return resources.ReservationStatus{}, resources.ErrReservationOwner
	}
	if row.state == "active" && !now.Before(row.expires) {
		return row.status(now), resources.ErrReservationExpired
	}
	if row.state == "active" {
		rev := row.revision + 1
		result, e := tx.conn.ExecContext(ctx, "UPDATE reservations SET revision=?,state='released',terminal_at_ns=? WHERE id=? AND revision=? AND state='active'", rev, now.UnixNano(), id, row.revision)
		if e != nil {
			return resources.ReservationStatus{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return resources.ReservationStatus{}, resources.ErrReservationConflict
		}
		if _, e = event(ctx, tx.conn, id, rev, "released", row.eventDigest, now, ref.ID); e != nil {
			return resources.ReservationStatus{}, e
		}
		row.revision, row.state, row.updated = rev, "released", now
	}
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationStatus{}, err
	}
	return row.status(now), nil
}
func (c *Coordinator) Status(ctx context.Context, id string, now time.Time) (resources.ReservationStatus, error) {
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	row, err := readRow(ctx, tx.conn, id)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationStatus{}, err
	}
	return row.status(now), nil
}
func (c *Coordinator) Snapshot(ctx context.Context, now time.Time) (resources.ReservationSnapshot, error) {
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationSnapshot{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationSnapshot{}, err
	}
	rows, err := tx.conn.QueryContext(ctx, "SELECT state,expires_at_ns,request FROM reservations ORDER BY id")
	if err != nil {
		return resources.ReservationSnapshot{}, err
	}
	out := resources.ReservationSnapshot{Version: resources.ReservationContractVersion, ObservedAt: now, DevicePools: []resources.ReservationPoolSnapshot{}}
	pools := map[string]resources.ReservationPoolSnapshot{}
	for rows.Next() {
		var state string
		var exp int64
		var body []byte
		var r resources.ReservationRequest
		if rows.Scan(&state, &exp, &body) != nil || json.Unmarshal(body, &r) != nil || r.Validate() != nil {
			rows.Close()
			return resources.ReservationSnapshot{}, resources.ErrReservation
		}
		if state == "released" {
			out.Released++
			continue
		}
		if state == "recovered" || now.UnixNano() >= exp {
			out.Expired++
			continue
		}
		out.Active++
		var ok bool
		if out.RAMBytes, ok = add(out.RAMBytes, r.RAMBytes); !ok {
			rows.Close()
			return resources.ReservationSnapshot{}, resources.ErrReservation
		}
		if r.GPUDevice == "" {
			out.AggregateVRAMBytes, ok = add(out.AggregateVRAMBytes, r.VRAMBytes)
		} else {
			p := pools[r.GPUDevice]
			p.Active++
			p.VRAMBytes, ok = add(p.VRAMBytes, r.VRAMBytes)
			pools[r.GPUDevice] = p
		}
		if !ok {
			rows.Close()
			return resources.ReservationSnapshot{}, resources.ErrReservation
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return resources.ReservationSnapshot{}, err
	}
	rows.Close()
	for _, p := range pools {
		out.DevicePools = append(out.DevicePools, p)
	}
	sort.Slice(out.DevicePools, func(i, j int) bool {
		if out.DevicePools[i].VRAMBytes != out.DevicePools[j].VRAMBytes {
			return out.DevicePools[i].VRAMBytes < out.DevicePools[j].VRAMBytes
		}
		return out.DevicePools[i].Active < out.DevicePools[j].Active
	})
	if out.Validate() != nil {
		return resources.ReservationSnapshot{}, resources.ErrReservation
	}
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationSnapshot{}, err
	}
	return out, nil
}

// Recover retains positive process-stop proof through the fencing commit.
func (c *Coordinator) Recover(ctx context.Context, id string, now time.Time) (resources.ReservationStatus, error) {
	var processID string
	var body []byte
	if err := c.db.QueryRowContext(ctx, "SELECT r.process_id,p.body FROM reservations r JOIN owner_processes p ON p.id=r.process_id WHERE r.id=? AND r.state='active'", id).Scan(&processID, &body); err != nil {
		return resources.ReservationStatus{}, err
	}
	var prior processguard.Reference
	if json.Unmarshal(body, &prior) != nil || prior.Validate() != nil || prior.ID != processID {
		return resources.ReservationStatus{}, ErrRecovery
	}
	current, err := processguard.Current(ctx)
	if err != nil || current.ID == processID {
		return resources.ReservationStatus{}, ErrRecovery
	}
	proof, err := processguard.Probe(ctx, prior)
	if err != nil {
		return resources.ReservationStatus{}, ErrRecovery
	}
	defer proof.Close()
	if proof.State != processguard.Unlocked || proof.ConfirmUnlocked(ctx) != nil {
		return resources.ReservationStatus{}, ErrRecovery
	}
	tx, err := c.begin(ctx)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	defer tx.rollback()
	now, err = clock(ctx, tx.conn, now)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	if err = registerProcess(ctx, tx.conn, current); err != nil {
		return resources.ReservationStatus{}, err
	}
	row, err := readRow(ctx, tx.conn, id)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	if row.state != "active" {
		if err = tx.commit(ctx); err != nil {
			return resources.ReservationStatus{}, err
		}
		return row.status(now), nil
	}
	if row.processID != processID || proof.ConfirmUnlocked(ctx) != nil {
		return resources.ReservationStatus{}, ErrRecovery
	}
	rev := row.revision + 1
	result, err := tx.conn.ExecContext(ctx, "UPDATE reservations SET revision=?,state='recovered',terminal_at_ns=? WHERE id=? AND revision=? AND state='active' AND process_id=?", rev, now.UnixNano(), id, row.revision, processID)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return resources.ReservationStatus{}, ErrRecovery
	}
	digest, err := event(ctx, tx.conn, id, rev, "recovered", row.eventDigest, now, current.ID)
	if err != nil {
		return resources.ReservationStatus{}, err
	}
	receipt := map[string]any{"version": 1, "reservation_id": id, "previous_process_id": processID, "recovering_process_id": current.ID, "event_digest": digest, "recovered_at": now}
	receiptBody, _ := json.Marshal(receipt)
	if _, err = tx.conn.ExecContext(ctx, "INSERT INTO reservation_recoveries VALUES(?,?,?,?,?,?,?)", id, rand.Text(), processID, current.ID, digest, now.UnixNano(), receiptBody); err != nil {
		return resources.ReservationStatus{}, err
	}
	if proof.ConfirmUnlocked(ctx) != nil {
		return resources.ReservationStatus{}, ErrRecovery
	}
	row.revision, row.state, row.updated = rev, "recovered", now
	if err = tx.commit(ctx); err != nil {
		return resources.ReservationStatus{}, err
	}
	return row.status(now), nil
}

// RecoverStopped privately sweeps active claims and recovers only owners for
// which processguard supplies positive stopped-process proof. Live, damaged or
// unverifiable owners remain fenced and their identifiers are never exposed.
func (c *Coordinator) RecoverStopped(ctx context.Context, now time.Time) (int, error) {
	if c == nil || c.db == nil || ctx == nil || ctx.Err() != nil {
		return 0, resources.ErrReservation
	}
	rows, err := c.db.QueryContext(ctx, "SELECT id FROM reservations WHERE state='active' ORDER BY id")
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	recovered := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return recovered, ctx.Err()
		}
		status, recoverErr := c.Recover(ctx, id, now)
		if recoverErr == nil {
			if status.State == resources.ReservationExpired {
				recovered++
			}
			continue
		}
		if errors.Is(recoverErr, ErrRecovery) || errors.Is(recoverErr, sql.ErrNoRows) || errors.Is(recoverErr, resources.ErrReservationExpired) {
			continue
		}
		return recovered, recoverErr
	}
	return recovered, nil
}

type stored struct {
	request                                          resources.ReservationRequest
	digest, state, processID, eventKind, eventDigest string
	revision                                         int64
	acquired, renewed, expires, updated              time.Time
}

func readRow(ctx context.Context, conn *sql.Conn, id string) (stored, error) {
	var r stored
	var body []byte
	var a, n, e int64
	var daemonID, hostScope, taskID, sessionID, providerID, modelID, profile, device, configDigest string
	var ramBytes, vramBytes, contextTokens int64
	var terminal sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT request_digest,revision,state,process_id,daemon_id,host_scope,task_id,session_id,provider_id,model_id,profile,device,ram_bytes,vram_bytes,context_tokens,config_digest,acquired_at_ns,last_renewed_at_ns,expires_at_ns,terminal_at_ns,request,COALESCE((SELECT kind FROM reservation_events WHERE reservation_id=reservations.id ORDER BY revision DESC LIMIT 1),''),COALESCE((SELECT event_digest FROM reservation_events WHERE reservation_id=reservations.id ORDER BY revision DESC LIMIT 1),'') FROM reservations WHERE id=?`, id).Scan(&r.digest, &r.revision, &r.state, &r.processID, &daemonID, &hostScope, &taskID, &sessionID, &providerID, &modelID, &profile, &device, &ramBytes, &vramBytes, &contextTokens, &configDigest, &a, &n, &e, &terminal, &body, &r.eventKind, &r.eventDigest)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(body, &r.request) != nil || r.request.Validate() != nil {
		return stored{}, resources.ErrReservation
	}
	if r.request.ReservationID != id || r.request.Owner.ProcessID != r.processID || r.request.Owner.DaemonID != daemonID ||
		r.request.HostScope != hostScope || r.request.TaskID != taskID || r.request.SessionID != sessionID ||
		r.request.ProviderID != providerID || r.request.ModelID != modelID || r.request.Profile != profile ||
		r.request.GPUDevice != device || ramBytes < 0 || vramBytes < 0 || contextTokens < 0 ||
		r.request.RAMBytes != uint64(ramBytes) || r.request.VRAMBytes != uint64(vramBytes) ||
		r.request.ContextTokens != int(contextTokens) || r.request.ConfigDigest != configDigest {
		return stored{}, resources.ErrReservation
	}
	digest, de := r.request.CanonicalDigest()
	if de != nil || digest != r.digest {
		return stored{}, resources.ErrReservation
	}
	r.acquired, r.renewed, r.expires = time.Unix(0, a).UTC(), time.Unix(0, n).UTC(), time.Unix(0, e).UTC()
	r.updated = r.renewed
	if terminal.Valid {
		r.updated = time.Unix(0, terminal.Int64).UTC()
	}
	wantKind := map[string]map[string]bool{"active": {"acquired": true, "renewed": true}, "released": {"released": true}, "recovered": {"recovered": true}}
	if !wantKind[r.state][r.eventKind] || r.revision < 1 || r.eventDigest == "" || r.renewed.Before(r.acquired) || !r.expires.After(r.renewed) {
		return stored{}, resources.ErrReservation
	}
	return r, nil
}
func (r stored) binding() resources.ReservationBinding {
	return resources.ReservationBinding{Version: resources.ReservationContractVersion, Request: r.request, RequestDigest: r.digest, AcquiredAt: r.acquired, ExpiresAt: r.expires}
}
func (r stored) status(now time.Time) resources.ReservationStatus {
	state := resources.ReservationActive
	if r.state == "released" {
		state = resources.ReservationReleased
	} else if r.state == "recovered" || !now.Before(r.expires) {
		state = resources.ReservationExpired
	}
	return resources.ReservationStatus{Version: resources.ReservationContractVersion, RequestDigest: r.digest, State: state, RAMBytes: r.request.RAMBytes, VRAMBytes: r.request.VRAMBytes, ContextTokens: r.request.ContextTokens, DeviceBound: r.request.GPUDevice != "", AcquiredAt: r.acquired, ExpiresAt: r.expires, UpdatedAt: r.updated}
}
func event(ctx context.Context, conn *sql.Conn, id string, revision int64, kind, previous string, now time.Time, processID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"version": 1, "reservation_id": id, "revision": revision, "kind": kind, "previous_digest": previous, "observed_at": now, "process_id": processID})
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	var prior any = previous
	if previous == "" {
		prior = nil
	}
	_, err := conn.ExecContext(ctx, "INSERT INTO reservation_events VALUES(?,?,?,?,?,?,?,?)", id, revision, kind, prior, digest, now.UnixNano(), processID, body)
	return digest, err
}
func add(a, b uint64) (uint64, bool) {
	if math.MaxUint64-a < b {
		return 0, false
	}
	return a + b, true
}

var _ resources.Coordinator = (*Coordinator)(nil)
