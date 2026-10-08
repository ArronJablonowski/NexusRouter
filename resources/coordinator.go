package resources

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

const reservationClockSkew = 5 * time.Second

type ReservationState string

const (
	ReservationActive   ReservationState = "active"
	ReservationReleased ReservationState = "released"
	ReservationExpired  ReservationState = "expired"
)

type reservationRecord struct {
	binding ReservationBinding
	state   ReservationState
	updated time.Time
	release func()
}

// ReservationStatus is safe for diagnostics: it excludes owner, host, task,
// session, provider, model, profile and device identifiers.
type ReservationStatus struct {
	Version       int              `json:"version"`
	RequestDigest string           `json:"request_digest"`
	State         ReservationState `json:"state"`
	RAMBytes      uint64           `json:"ram_bytes"`
	VRAMBytes     uint64           `json:"vram_bytes"`
	ContextTokens int              `json:"context_tokens"`
	DeviceBound   bool             `json:"device_bound"`
	AcquiredAt    time.Time        `json:"acquired_at"`
	ExpiresAt     time.Time        `json:"expires_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

func (s ReservationStatus) Validate() error {
	if s.Version != ReservationContractVersion || !reservationDigest(s.RequestDigest) ||
		(s.State != ReservationActive && s.State != ReservationReleased && s.State != ReservationExpired) ||
		s.RAMBytes == 0 || s.ContextTokens < 1 || !reservationTime(s.AcquiredAt) ||
		!reservationTime(s.ExpiresAt) || !reservationTime(s.UpdatedAt) ||
		s.AcquiredAt.Location() != time.UTC || s.ExpiresAt.Location() != time.UTC || s.UpdatedAt.Location() != time.UTC ||
		s.ExpiresAt.Before(s.AcquiredAt) || s.UpdatedAt.Before(s.AcquiredAt) ||
		(s.DeviceBound && s.VRAMBytes == 0) {
		return ErrReservation
	}
	return nil
}

// ReservationPoolSnapshot reports one anonymized device pool. Active counts
// unreleased capacity holders, including expired executions. Pool ordering is
// deterministic, but no stable hardware identifier is disclosed.
type ReservationPoolSnapshot struct {
	Active    int    `json:"active"`
	VRAMBytes uint64 `json:"vram_bytes"`
}

// ReservationSnapshot is an identifier-free coordinator observation. Expired
// execution authority does not release capacity: byte totals include expired,
// unreleased holders, while Active counts only unexpired execution grants.
type ReservationSnapshot struct {
	Version            int                       `json:"version"`
	ObservedAt         time.Time                 `json:"observed_at"`
	Active             int                       `json:"active"`
	Released           int                       `json:"released"`
	Expired            int                       `json:"expired"`
	RAMBytes           uint64                    `json:"ram_bytes"`
	AggregateVRAMBytes uint64                    `json:"aggregate_vram_bytes"`
	DevicePools        []ReservationPoolSnapshot `json:"device_pools"`
}

// Coordinator is the common admission lifecycle implemented by the local
// fast-path adapter and by host-scoped durable coordinators.
type Coordinator interface {
	Acquire(context.Context, Snapshot, ReservationRequest, time.Time) (ReservationBinding, error)
	Renew(context.Context, string, ReservationOwner, time.Time, time.Duration) (ReservationBinding, error)
	Release(context.Context, string, ReservationOwner, time.Time) (ReservationStatus, error)
	Status(context.Context, string, time.Time) (ReservationStatus, error)
	Snapshot(context.Context, time.Time) (ReservationSnapshot, error)
}

func (s ReservationSnapshot) Validate() error {
	if s.Version != ReservationContractVersion || !reservationTime(s.ObservedAt) || s.ObservedAt.Location() != time.UTC ||
		s.Active < 0 || s.Released < 0 || s.Expired < 0 || s.DevicePools == nil {
		return ErrReservation
	}
	for index, pool := range s.DevicePools {
		if pool.Active < 1 || pool.VRAMBytes == 0 || index > 0 &&
			(s.DevicePools[index-1].VRAMBytes > pool.VRAMBytes ||
				s.DevicePools[index-1].VRAMBytes == pool.VRAMBytes && s.DevicePools[index-1].Active > pool.Active) {
			return ErrReservation
		}
	}
	return nil
}

// InMemoryCoordinator adds durable-contract semantics without replacing the
// existing Budget fast path. It is process-local; a durable implementation can
// implement the same lifecycle while serializing claims across processes.
type InMemoryCoordinator struct {
	mu      sync.Mutex
	budget  *Budget
	records map[string]*reservationRecord
}

var _ Coordinator = (*InMemoryCoordinator)(nil)

func NewInMemoryCoordinator(budget *Budget) (*InMemoryCoordinator, error) {
	if budget == nil {
		return nil, ErrReservation
	}
	return &InMemoryCoordinator{budget: budget, records: map[string]*reservationRecord{}}, nil
}

func (c *InMemoryCoordinator) Acquire(ctx context.Context, snapshot Snapshot, request ReservationRequest, now time.Time) (ReservationBinding, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || request.Validate() != nil || !coordinatorTime(now) {
		return ReservationBinding{}, errors.Join(ErrReservation, contextError(ctx))
	}
	now = now.UTC()
	request = normalizeReservationRequest(request)
	digest, err := request.CanonicalDigest()
	if err != nil {
		return ReservationBinding{}, ErrReservation
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now)
	if existing := c.records[request.ReservationID]; existing != nil {
		if existing.binding.RequestDigest != digest {
			return ReservationBinding{}, ErrReservationConflict
		}
		if existing.state != ReservationActive || !now.Before(existing.binding.ExpiresAt) {
			return existing.binding, ErrReservationExpired
		}
		return existing.binding, nil
	}
	if delta := now.Sub(request.RequestedAt); delta < -reservationClockSkew || delta > reservationClockSkew {
		return ReservationBinding{}, ErrReservation
	}
	release, err := c.budget.Reserve(snapshot, Need{BackendManagedRAM: request.BackendManagedRAM, RAM: request.RAMBytes, VRAM: request.VRAMBytes, Device: request.GPUDevice}, now)
	if err != nil {
		return ReservationBinding{}, err
	}
	binding := ReservationBinding{Version: ReservationContractVersion, Request: request, RequestDigest: digest, AcquiredAt: now, ExpiresAt: now.Add(request.TTL).UTC()}
	if binding.Validate() != nil {
		release()
		return ReservationBinding{}, ErrReservation
	}
	c.records[request.ReservationID] = &reservationRecord{binding: binding, state: ReservationActive, updated: now, release: release}
	return binding, nil
}

// Renew extends one active reservation. It never shortens an expiry and exact
// owner identity is required even when the reservation is already terminal.
func (c *InMemoryCoordinator) Renew(ctx context.Context, reservationID string, owner ReservationOwner, now time.Time, ttl time.Duration) (ReservationBinding, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || !reservationText(reservationID, 128) || owner.Validate() != nil ||
		!coordinatorTime(now) || ttl < minReservationTTL || ttl > maxReservationTTL || ttl%time.Millisecond != 0 {
		return ReservationBinding{}, errors.Join(ErrReservation, contextError(ctx))
	}
	now = now.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now)
	record := c.records[reservationID]
	if record == nil {
		return ReservationBinding{}, ErrReservation
	}
	if record.binding.Request.Owner != owner {
		return ReservationBinding{}, ErrReservationOwner
	}
	if record.state != ReservationActive {
		return record.binding, ErrReservationExpired
	}
	if now.Before(record.binding.AcquiredAt.Add(-reservationClockSkew)) {
		return ReservationBinding{}, ErrReservation
	}
	next := now.Add(ttl).UTC()
	if next.After(record.binding.ExpiresAt) {
		record.binding.ExpiresAt = next
	}
	record.updated = reservationUpdateTime(record.binding.AcquiredAt, now)
	return record.binding, nil
}

func (c *InMemoryCoordinator) Release(ctx context.Context, reservationID string, owner ReservationOwner, now time.Time) (ReservationStatus, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || !reservationText(reservationID, 128) || owner.Validate() != nil || !coordinatorTime(now) {
		return ReservationStatus{}, errors.Join(ErrReservation, contextError(ctx))
	}
	now = now.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now)
	record := c.records[reservationID]
	if record == nil {
		return ReservationStatus{}, ErrReservation
	}
	if record.binding.Request.Owner != owner {
		return ReservationStatus{}, ErrReservationOwner
	}
	expired := record.state == ReservationExpired
	if now.Before(record.binding.AcquiredAt.Add(-reservationClockSkew)) {
		return ReservationStatus{}, ErrReservation
	}
	if record.state == ReservationActive || expired {
		record.release()
		record.release = nil
		record.state = ReservationReleased
		record.updated = reservationUpdateTime(record.binding.AcquiredAt, now)
	}
	if expired {
		return statusFromRecord(record), ErrReservationExpired
	}
	return statusFromRecord(record), nil
}

func (c *InMemoryCoordinator) Status(ctx context.Context, reservationID string, now time.Time) (ReservationStatus, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || !reservationText(reservationID, 128) || !coordinatorTime(now) {
		return ReservationStatus{}, errors.Join(ErrReservation, contextError(ctx))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now.UTC())
	record := c.records[reservationID]
	if record == nil {
		return ReservationStatus{}, ErrReservation
	}
	return statusFromRecord(record), nil
}

func (c *InMemoryCoordinator) Snapshot(ctx context.Context, now time.Time) (ReservationSnapshot, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || !coordinatorTime(now) {
		return ReservationSnapshot{}, errors.Join(ErrReservation, contextError(ctx))
	}
	now = now.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(now)
	result := ReservationSnapshot{Version: ReservationContractVersion, ObservedAt: now, DevicePools: []ReservationPoolSnapshot{}}
	devicePools := map[string]ReservationPoolSnapshot{}
	for _, record := range c.records {
		switch record.state {
		case ReservationReleased:
			result.Released++
		case ReservationExpired, ReservationActive:
			request := record.binding.Request
			if record.state == ReservationExpired {
				result.Expired++
			} else {
				result.Active++
			}
			var ok bool
			result.RAMBytes, ok = addReservationBytes(result.RAMBytes, request.RAMBytes)
			if !ok {
				return ReservationSnapshot{}, ErrReservation
			}
			if request.GPUDevice == "" {
				result.AggregateVRAMBytes, ok = addReservationBytes(result.AggregateVRAMBytes, request.VRAMBytes)
				if !ok {
					return ReservationSnapshot{}, ErrReservation
				}
			} else {
				pool := devicePools[request.GPUDevice]
				pool.Active++
				pool.VRAMBytes, ok = addReservationBytes(pool.VRAMBytes, request.VRAMBytes)
				if !ok {
					return ReservationSnapshot{}, ErrReservation
				}
				devicePools[request.GPUDevice] = pool
			}
		}
	}
	for _, pool := range devicePools {
		result.DevicePools = append(result.DevicePools, pool)
	}
	sort.Slice(result.DevicePools, func(i, j int) bool {
		if result.DevicePools[i].VRAMBytes != result.DevicePools[j].VRAMBytes {
			return result.DevicePools[i].VRAMBytes < result.DevicePools[j].VRAMBytes
		}
		return result.DevicePools[i].Active < result.DevicePools[j].Active
	})
	if result.Validate() != nil {
		return ReservationSnapshot{}, ErrReservation
	}
	return result, nil
}

func (c *InMemoryCoordinator) expireLocked(now time.Time) {
	for _, record := range c.records {
		if record.state == ReservationActive && !now.Before(record.binding.ExpiresAt) {
			// Retain the budget charge until the exact owner joins execution
			// and releases it. Cancellation/expiry is not termination proof.
			record.state = ReservationExpired
			record.updated = now
		}
	}
}

func statusFromRecord(record *reservationRecord) ReservationStatus {
	request := record.binding.Request
	return ReservationStatus{Version: ReservationContractVersion, RequestDigest: record.binding.RequestDigest, State: record.state,
		RAMBytes: request.RAMBytes, VRAMBytes: request.VRAMBytes, ContextTokens: request.ContextTokens,
		DeviceBound: request.GPUDevice != "", AcquiredAt: record.binding.AcquiredAt,
		ExpiresAt: record.binding.ExpiresAt, UpdatedAt: record.updated}
}

func coordinatorTime(value time.Time) bool { return reservationTime(value) }

func reservationUpdateTime(acquired, observed time.Time) time.Time {
	if observed.Before(acquired) {
		return acquired
	}
	return observed
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func addReservationBytes(left, right uint64) (uint64, bool) {
	if ^uint64(0)-left < right {
		return 0, false
	}
	return left + right, true
}
