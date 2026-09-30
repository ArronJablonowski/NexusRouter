package metrics

import (
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

// ResourceMeasurement is one fixed, identifier-free host measurement. An
// unavailable value is explicit and always zero, so unsupported probes cannot
// be confused with an observed zero.
type ResourceMeasurement struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Value     int64  `json:"value"`
}

// Resources is an optional live observation attached by the application
// service. Storage-only snapshots omit it because reading a database is not a
// host measurement.
type Resources struct {
	ObservedAt   time.Time             `json:"observed_at"`
	Measurements []ResourceMeasurement `json:"measurements"`
}

type resourceDefinition struct {
	name string
	unit string
}

var resourceDefinitions = []resourceDefinition{
	{"cpu_threads", "{thread}"},
	{"ram_total_bytes", "By"},
	{"ram_available_bytes", "By"},
	{"swap_used_bytes", "By"},
	{"vram_total_bytes", "By"},
	{"vram_available_bytes", "By"},
	{"thermal_pressure", "{bool}"},
	{"unified_memory", "{bool}"},
	{"reservation_coordinator", "{bool}"},
	{"reservations_active", "{reservation}"},
	{"reservations_expired", "{reservation}"},
	{"reservations_released", "{reservation}"},
	{"reserved_ram_bytes", "By"},
	{"reserved_vram_bytes", "By"},
}

// UnavailableResources creates the canonical explicit-unavailability block.
func UnavailableResources(at time.Time) Resources {
	r := Resources{ObservedAt: at, Measurements: make([]ResourceMeasurement, len(resourceDefinitions))}
	for i, def := range resourceDefinitions {
		r.Measurements[i].Name = def.name
	}
	return r
}

// ResourcesFromSnapshot removes profiler provenance and device identity while
// retaining bounded capacity and pressure measurements.
func ResourcesFromSnapshot(snapshot resources.Snapshot) (Resources, error) {
	r := UnavailableResources(snapshot.Time)
	set := func(index int, value uint64) bool {
		if value > math.MaxInt64 {
			return false
		}
		r.Measurements[index].Available = true
		r.Measurements[index].Value = int64(value)
		return true
	}
	if snapshot.Time.IsZero() || snapshot.Time.Year() < 1 || snapshot.Time.Year() > 9999 {
		return Resources{}, ErrInvalid
	}
	if snapshot.CPUs < 0 {
		return Resources{}, ErrInvalid
	}
	if snapshot.CPUs > 0 {
		if !set(0, uint64(snapshot.CPUs)) {
			return Resources{}, ErrInvalid
		}
	}
	if snapshot.TotalRAM > 0 {
		if snapshot.AvailableRAM > snapshot.TotalRAM || !set(1, snapshot.TotalRAM) || !set(2, snapshot.AvailableRAM) {
			return Resources{}, ErrInvalid
		}
	} else if snapshot.AvailableRAM != 0 {
		return Resources{}, ErrInvalid
	}
	if snapshot.SwapUsed != nil && !set(3, *snapshot.SwapUsed) {
		return Resources{}, ErrInvalid
	}
	if (snapshot.VRAMTotal == nil) != (snapshot.VRAMAvailable == nil) {
		return Resources{}, ErrInvalid
	}
	if snapshot.VRAMTotal != nil {
		if *snapshot.VRAMTotal == 0 || *snapshot.VRAMAvailable > *snapshot.VRAMTotal || !set(4, *snapshot.VRAMTotal) || !set(5, *snapshot.VRAMAvailable) {
			return Resources{}, ErrInvalid
		}
	}
	if snapshot.ThermalPressure != nil {
		r.Measurements[6].Available = true
		if *snapshot.ThermalPressure {
			r.Measurements[6].Value = 1
		}
	}
	// Unified memory is a property of the successfully observed memory profile,
	// not an independently probed capacity.
	if r.Measurements[1].Available {
		r.Measurements[7].Available = true
		if snapshot.UnifiedMemory {
			r.Measurements[7].Value = 1
		}
	}
	if r.validate(time.Time{}) != nil {
		return Resources{}, ErrInvalid
	}
	return r, nil
}

// WithReservationSnapshot attaches fixed, identifier-free durable admission
// gauges. It never exposes reservation, owner, task, model, device or config
// identities.
func WithReservationSnapshot(r Resources, snapshot resources.ReservationSnapshot) (Resources, error) {
	if r.validate(time.Time{}) != nil || snapshot.Validate() != nil || snapshot.Active > math.MaxInt64 || snapshot.Expired > math.MaxInt64 || snapshot.Released > math.MaxInt64 {
		return Resources{}, ErrInvalid
	}
	reservedVRAM := snapshot.AggregateVRAMBytes
	for _, pool := range snapshot.DevicePools {
		if math.MaxUint64-reservedVRAM < pool.VRAMBytes {
			return Resources{}, ErrInvalid
		}
		reservedVRAM += pool.VRAMBytes
	}
	values := []uint64{1, uint64(snapshot.Active), uint64(snapshot.Expired), uint64(snapshot.Released), snapshot.RAMBytes, reservedVRAM}
	for offset, value := range values {
		if value > math.MaxInt64 {
			return Resources{}, ErrInvalid
		}
		measurement := &r.Measurements[8+offset]
		measurement.Available = true
		measurement.Value = int64(value)
	}
	if r.validate(time.Time{}) != nil {
		return Resources{}, ErrInvalid
	}
	return r, nil
}

func (s Snapshot) validateResources() error {
	return s.Resources.validate(s.ObservedAt)
}

func (r Resources) validate(parent time.Time) error {
	if r.ObservedAt.IsZero() || r.ObservedAt.Year() < 1 || r.ObservedAt.Year() > 9999 || len(r.Measurements) != len(resourceDefinitions) {
		return ErrInvalid
	}
	if _, err := r.ObservedAt.MarshalJSON(); err != nil || (!parent.IsZero() && r.ObservedAt.After(parent)) {
		return ErrInvalid
	}
	for i, def := range resourceDefinitions {
		m := r.Measurements[i]
		if m.Name != def.name || m.Value < 0 || (!m.Available && m.Value != 0) {
			return ErrInvalid
		}
		if (i == 6 || i == 7 || i == 8) && m.Value > 1 {
			return ErrInvalid
		}
	}
	if r.Measurements[1].Available != r.Measurements[2].Available || r.Measurements[4].Available != r.Measurements[5].Available {
		return ErrInvalid
	}
	if r.Measurements[1].Available && (r.Measurements[1].Value == 0 || r.Measurements[2].Value > r.Measurements[1].Value) {
		return ErrInvalid
	}
	if r.Measurements[4].Available && (r.Measurements[4].Value == 0 || r.Measurements[5].Value > r.Measurements[4].Value) {
		return ErrInvalid
	}
	return nil
}
