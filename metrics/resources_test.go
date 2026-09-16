package metrics

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/resources"
)

func TestResourcesFromSnapshotFixedVocabularyAndOwnership(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	swap, vramTotal, vramAvailable, pressure := uint64(7), uint64(100), uint64(40), true
	source := resources.Snapshot{
		Time: at, CPUs: 12, TotalRAM: 1000, AvailableRAM: 600, SwapUsed: &swap,
		VRAMTotal: &vramTotal, VRAMAvailable: &vramAvailable, ThermalPressure: &pressure,
		UnifiedMemory: true, Source: "private-source",
		GPUs: &resources.GPUInventory{Sources: []resources.GPUObservation{{Devices: []resources.GPUDevice{{ID: "private-device"}}}}},
	}
	got, err := ResourcesFromSnapshot(source)
	if err != nil || got.validate(at) != nil {
		t.Fatal(got, err)
	}
	want := []int64{12, 1000, 600, 7, 100, 40, 1, 1}
	for i, measurement := range got.Measurements[:8] {
		if !measurement.Available || measurement.Name != resourceDefinitions[i].name || measurement.Value != want[i] {
			t.Fatal(i, measurement)
		}
	}
	for i, measurement := range got.Measurements[8:] {
		if measurement.Available || measurement.Value != 0 {
			t.Fatal(i+8, measurement)
		}
	}
	*source.SwapUsed = 99
	*source.VRAMTotal = 999
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "private") || got.Measurements[3].Value != 7 || got.Measurements[4].Value != 100 {
		t.Fatal(string(body))
	}
}

func TestWithReservationSnapshotUsesFixedIdentifierFreeGauges(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	base, err := ResourcesFromSnapshot(resources.Snapshot{Time: at, CPUs: 8, TotalRAM: 100, AvailableRAM: 50})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := WithReservationSnapshot(base, resources.ReservationSnapshot{
		Version: 1, ObservedAt: at, Active: 2, Expired: 3, Released: 4, RAMBytes: 20,
		AggregateVRAMBytes: 5, DevicePools: []resources.ReservationPoolSnapshot{{Active: 1, VRAMBytes: 7}},
	})
	if err != nil || attached.validate(at) != nil {
		t.Fatal(attached, err)
	}
	want := []int64{1, 2, 3, 4, 20, 12}
	for index, value := range want {
		measurement := attached.Measurements[8+index]
		if !measurement.Available || measurement.Value != value {
			t.Fatal(index, measurement)
		}
	}
	body, _ := json.Marshal(attached)
	for _, forbidden := range []string{"task-", "model-", "provider-", "gpu-", "process-", "config-"} {
		if strings.Contains(strings.ToLower(string(body)), forbidden) {
			t.Fatal("identifier leaked", string(body))
		}
	}
}

func TestUnavailableResourcesAndValidation(t *testing.T) {
	at := time.Now().UTC()
	r := UnavailableResources(at)
	s := NewSnapshot(29, at)
	s.Resources = &r
	if s.Validate() != nil {
		t.Fatal(s)
	}
	for _, measurement := range r.Measurements {
		if measurement.Available || measurement.Value != 0 {
			t.Fatal(measurement)
		}
	}
	cases := map[string]func(*Resources){
		"future":   func(r *Resources) { r.ObservedAt = at.Add(time.Second) },
		"name":     func(r *Resources) { r.Measurements[0].Name = "device_id" },
		"missing":  func(r *Resources) { r.Measurements = r.Measurements[1:] },
		"negative": func(r *Resources) { r.Measurements[0].Available = true; r.Measurements[0].Value = -1 },
		"invented": func(r *Resources) { r.Measurements[0].Value = 1 },
		"boolean":  func(r *Resources) { r.Measurements[6].Available = true; r.Measurements[6].Value = 2 },
		"ram pair": func(r *Resources) { r.Measurements[1].Available = true; r.Measurements[1].Value = 1 },
		"ram overflow": func(r *Resources) {
			r.Measurements[1].Available = true
			r.Measurements[1].Value = 1
			r.Measurements[2].Available = true
			r.Measurements[2].Value = 2
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := UnavailableResources(at)
			mutate(&bad)
			s := NewSnapshot(29, at)
			s.Resources = &bad
			if s.Validate() == nil {
				t.Fatal("invalid resources accepted")
			}
		})
	}
}

func TestResourcesFromSnapshotRejectsInvalidCapacity(t *testing.T) {
	at := time.Now().UTC()
	huge := uint64(math.MaxInt64) + 1
	for _, snapshot := range []resources.Snapshot{
		{Time: at, CPUs: -1},
		{Time: at, TotalRAM: 1, AvailableRAM: 2},
		{Time: at, TotalRAM: huge},
		{Time: at, VRAMTotal: &huge},
	} {
		if _, err := ResourcesFromSnapshot(snapshot); err == nil {
			t.Fatal("invalid profile accepted", snapshot)
		}
	}
}

func TestMarshalOTLPResources(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	r, err := ResourcesFromSnapshot(resources.Snapshot{Time: at, CPUs: 8, TotalRAM: 100, AvailableRAM: 25})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSnapshot(1, at)
	s.Resources = &r
	body, err := MarshalOTLP(s)
	if err != nil {
		t.Fatal(err)
	}
	var request otlpRequest
	if json.Unmarshal(body, &request) != nil {
		t.Fatal(string(body))
	}
	items := request.ResourceMetrics[0].ScopeMetrics[0].Metrics
	base := 0
	for _, group := range s.Groups {
		if group.Available {
			base++
		}
	}
	if len(items) != base+5 {
		t.Fatal(len(items), string(body))
	}
	want := []string{"darwinrouter.resource.cpu_threads", "darwinrouter.resource.ram_total_bytes", "darwinrouter.resource.ram_available_bytes", "darwinrouter.resource.unified_memory", "darwinrouter.resource.available"}
	for i, name := range want {
		if items[base+i].Name != name {
			t.Fatal(i, items[base+i].Name)
		}
	}
	if len(items[len(items)-1].Gauge.DataPoints) != len(resourceDefinitions) {
		t.Fatal(items[len(items)-1])
	}
}
