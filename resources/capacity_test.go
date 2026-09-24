package resources

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"
)

func capacityFixture(t *testing.T) (*Budget, CapacityRequest) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	swap, swapPressure, thermal := uint64(0), false, false
	budget, err := NewAdaptiveBudget(Limits{MaxConcurrent: 4, RAMPercent: 80, VRAMPercent: 80, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	request := CapacityRequest{Version: 1, Now: now, Need: Need{RAM: 1 << 30}, Snapshot: Snapshot{Time: now, CPUs: 8, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, SwapUsed: &swap, SwapPressure: &swapPressure, ThermalPressure: &thermal, Source: "fixture"}}
	return budget, request
}

func TestCapacityPlanAccountsForLiveReservationsWithoutMutation(t *testing.T) {
	budget, request := capacityFixture(t)
	live, err := budget.Reserve(request.Snapshot, request.Need, request.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer live()
	result, err := budget.Plan(context.Background(), request)
	room, _ := headroom(request.Snapshot.TotalRAM, request.Snapshot.AvailableRAM, request.Need.RAM, 80)
	if err != nil || result.Validate() != nil || result.Action != CapacityAdmit || result.Reason != CapacityAvailable || result.MaxAdditional != 3 || result.Headroom.RAMBytes != room || result.Headroom.VRAMKnown {
		t.Fatal(result, err)
	}
	// Planning did not reserve: all three advertised slots remain available.
	var releases []func()
	for range result.MaxAdditional {
		release, err := budget.Reserve(request.Snapshot, request.Need, request.Now)
		if err != nil {
			t.Fatal("advertised capacity unavailable", err)
		}
		releases = append(releases, release)
	}
	if release, err := budget.Reserve(request.Snapshot, request.Need, request.Now); !errors.Is(err, ErrCapacity) || release != nil {
		t.Fatal("plan exceeded real budget", err)
	}
	for _, release := range releases {
		release()
	}
}

func TestCapacityPlanPreservesSwapGrowthBaseline(t *testing.T) {
	budget, request := capacityFixture(t)
	release, err := budget.Reserve(request.Snapshot, request.Need, request.Now)
	if err != nil {
		t.Fatal(err)
	}
	release()
	*request.Snapshot.SwapUsed = (5 << 30) + 1
	plan, err := budget.Plan(context.Background(), request)
	if err != nil || plan.Action != CapacityWait || plan.Reason != CapacitySwap || plan.MaxAdditional != 0 {
		t.Fatalf("planner forgot admitted swap baseline: %+v %v", plan, err)
	}
	if release, err := budget.Reserve(request.Snapshot, request.Need, request.Now); !errors.Is(err, ErrCapacity) || release != nil {
		t.Fatalf("plan changed the actual swap baseline: %v", err)
	}
}

func TestCapacityPlanPressureWaitsWithoutClaimingCapacity(t *testing.T) {
	for _, mode := range []string{"thermal", "swap", "full"} {
		t.Run(mode, func(t *testing.T) {
			budget, request := capacityFixture(t)
			want := CapacityExhausted
			switch mode {
			case "thermal":
				*request.Snapshot.ThermalPressure = true
				want = CapacityThermal
			case "swap":
				*request.Snapshot.SwapUsed = 1
				*request.Snapshot.SwapPressure = true
				want = CapacitySwap
			case "full":
				for range 4 {
					if _, err := budget.Reserve(request.Snapshot, request.Need, request.Now); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := budget.Plan(context.Background(), request)
			if err != nil || result.Validate() != nil || result.Action != CapacityWait || result.Reason != want || result.MaxAdditional != 0 {
				t.Fatal(result, err)
			}
		})
	}
}

func TestCapacityPlanDoesNotTreatColdSwapAsPressure(t *testing.T) {
	budget, request := capacityFixture(t)
	*request.Snapshot.SwapUsed = 8 << 30
	result, err := budget.Plan(context.Background(), request)
	if err != nil || result.Action != CapacityAdmit || result.MaxAdditional == 0 {
		t.Fatal(result, err)
	}
}

func TestCapacityPlanNormalizesEquivalentTimezones(t *testing.T) {
	budget, request := capacityFixture(t)
	zone := time.FixedZone("equivalent", -7*60*60)
	request.Now = request.Now.In(zone)
	request.Snapshot.Time = request.Snapshot.Time.In(zone)
	result, err := budget.Plan(context.Background(), request)
	if err != nil || result.Validate() != nil || result.SnapshotTime.Location() != time.UTC || result.ObservedAt.Location() != time.UTC {
		t.Fatal(result, err)
	}
}

func TestCapacityPlanDeviceHeadroomMatchesAdaptiveBudget(t *testing.T) {
	budget, request := capacityFixture(t)
	request.Need = Need{RAM: 1 << 30, VRAM: 2 << 30, Device: "nvidia:GPU-abcdef01"}
	request.Snapshot.GPUs = &GPUInventory{Time: request.Now, Sources: []GPUObservation{
		{Source: "nvidia-smi", Status: "observed", Devices: []GPUDevice{{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: 16 << 30, AvailableBytes: 16 << 30}}},
	}}
	before := request.Snapshot.GPUs.Sources[0].Devices[0]
	result, err := budget.Plan(context.Background(), request)
	if err != nil || result.Validate() != nil || result.Action != CapacityAdmit || result.MaxAdditional != 1 || !result.Headroom.VRAMKnown || result.Headroom.Device != request.Need.Device {
		t.Fatal(result, err)
	}
	request.Snapshot.GPUs.Sources[0].Devices[0].AvailableBytes = 0
	if result.Headroom.VRAMBytes == 0 || before.AvailableBytes == 0 {
		t.Fatal("result aliases input or omitted device headroom", result)
	}
}

func TestCapacityPlanRejectsUnknownStaleAndImpossibleData(t *testing.T) {
	mutations := map[string]func(*CapacityRequest){
		"request-version": func(r *CapacityRequest) { r.Version = 2 },
		"zero-need":       func(r *CapacityRequest) { r.Need.RAM = 0 },
		"device-no-vram":  func(r *CapacityRequest) { r.Need.Device = "nvidia:GPU-abcdef01" },
		"stale":           func(r *CapacityRequest) { r.Snapshot.Time = r.Now.Add(-2 * time.Minute) },
		"future":          func(r *CapacityRequest) { r.Snapshot.Time = r.Now.Add(time.Nanosecond) },
		"ram":             func(r *CapacityRequest) { r.Snapshot.AvailableRAM = r.Snapshot.TotalRAM + 1 },
		"cpu":             func(r *CapacityRequest) { r.Snapshot.CPUs = -1 },
		"source":          func(r *CapacityRequest) { r.Snapshot.Source = "bad\nsource" },
		"partial-vram":    func(r *CapacityRequest) { total := uint64(1); r.Snapshot.VRAMTotal = &total },
		"unknown-vram-thermal-pressure": func(r *CapacityRequest) {
			r.Need.VRAM = 1
			*r.Snapshot.ThermalPressure = true
		},
		"unknown-vram-swap-pressure": func(r *CapacityRequest) {
			r.Need.VRAM = 1
			*r.Snapshot.SwapPressure = true
		},
		"unified-vram": func(r *CapacityRequest) {
			total, available := uint64(1), uint64(1)
			r.Snapshot.UnifiedMemory, r.Snapshot.VRAMTotal, r.Snapshot.VRAMAvailable = true, &total, &available
		},
		"thermal-mismatch": func(r *CapacityRequest) { r.Snapshot.ThermalState = "critical" },
		"bad-gpu": func(r *CapacityRequest) {
			r.Snapshot.GPUs = &GPUInventory{Time: r.Now, Sources: []GPUObservation{{Source: "nvidia-smi", Status: "observed", Devices: []GPUDevice{{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: 1, AvailableBytes: 2}}}}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			budget, request := capacityFixture(t)
			mutate(&request)
			before := request
			result, err := budget.Plan(context.Background(), request)
			if !errors.Is(err, ErrResourceData) || result.Version != 0 || !reflect.DeepEqual(request, before) {
				t.Fatal(result, err)
			}
		})
	}
	budget, request := capacityFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.Plan(canceled, request); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrResourceData) {
		t.Fatal(err)
	}
}

func TestCapacityResultValidation(t *testing.T) {
	_, request := capacityFixture(t)
	valid := CapacityResult{Version: 1, Action: CapacityAdmit, Reason: CapacityAvailable, SnapshotTime: request.Now, ObservedAt: request.Now, Headroom: CapacityHeadroom{RAMBytes: 1}, MaxAdditional: 1}
	if valid.Validate() != nil {
		t.Fatal(valid.Validate())
	}
	for _, mutate := range []func(*CapacityResult){
		func(r *CapacityResult) { r.Version = 2 },
		func(r *CapacityResult) { r.Action = "execute" },
		func(r *CapacityResult) { r.Reason = CapacityThermal },
		func(r *CapacityResult) { r.MaxAdditional = 0 },
		func(r *CapacityResult) { r.Headroom.RAMBytes = 0 },
		func(r *CapacityResult) { r.Headroom.VRAMBytes = 1 },
		func(r *CapacityResult) { r.Headroom.Device = "nvidia:GPU-abcdef01" },
		func(r *CapacityResult) { r.ObservedAt = r.SnapshotTime.Add(-time.Second) },
	} {
		bad := valid
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid result accepted", bad)
		}
	}
}

func TestCapacityPlanMatchesReservationProperty(t *testing.T) {
	const gib = uint64(1 << 30)
	random := rand.New(rand.NewSource(0xDA75))
	for iteration := range 300 {
		now := time.Unix(1_800_000_000+int64(iteration), 0).UTC()
		limits := Limits{MaxConcurrent: 1 + random.Intn(8), RAMPercent: float64(50 + random.Intn(51)), VRAMPercent: float64(50 + random.Intn(51)), MaxAge: time.Minute}
		var budget *Budget
		var err error
		if random.Intn(2) == 0 {
			budget, err = NewBudget(limits)
		} else {
			budget, err = NewAdaptiveBudget(limits)
		}
		if err != nil {
			t.Fatal(err)
		}
		totalRAM := uint64(8+random.Intn(121)) * gib
		availableRAM := uint64(random.Intn(int(totalRAM/gib)+1)) * gib
		swap, swapPressure, thermal := uint64(random.Intn(9))*gib, false, false
		snapshot := Snapshot{Time: now, CPUs: 1 + random.Intn(16), TotalRAM: totalRAM, AvailableRAM: availableRAM, SwapUsed: &swap, SwapPressure: &swapPressure, ThermalPressure: &thermal, Source: "property"}
		need := Need{RAM: uint64(1+random.Intn(8)) * gib}
		switch iteration % 3 {
		case 0:
			snapshot.UnifiedMemory = true
		case 1:
			total, available := uint64(8+random.Intn(57))*gib, uint64(1+random.Intn(8))*gib
			if available > total {
				available = total
			}
			snapshot.VRAMTotal, snapshot.VRAMAvailable = &total, &available
			need.VRAM = uint64(1+random.Intn(4)) * gib
		case 2:
			total, available := uint64(8+random.Intn(57))*gib, uint64(1+random.Intn(8))*gib
			if available > total {
				available = total
			}
			need.VRAM, need.Device = uint64(1+random.Intn(4))*gib, "nvidia:GPU-abcdef01"
			snapshot.GPUs = &GPUInventory{Time: now, Sources: []GPUObservation{{Source: "nvidia-smi", Status: "observed", Devices: []GPUDevice{{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: total, AvailableBytes: available}}}}}
		}
		var held []func()
		for range random.Intn(limits.MaxConcurrent + 1) {
			release, reserveErr := budget.Reserve(snapshot, need, now)
			if reserveErr != nil {
				break
			}
			held = append(held, release)
		}
		result, planErr := budget.Plan(context.Background(), CapacityRequest{Version: 1, Snapshot: snapshot, Need: need, Now: now})
		if planErr != nil || result.Validate() != nil {
			t.Fatalf("iteration %d: result=%+v err=%v", iteration, result, planErr)
		}
		var planned []func()
		for slot := 0; slot < result.MaxAdditional; slot++ {
			release, reserveErr := budget.Reserve(snapshot, need, now)
			if reserveErr != nil {
				t.Fatalf("iteration %d slot %d/%d: %v", iteration, slot, result.MaxAdditional, reserveErr)
			}
			planned = append(planned, release)
		}
		if release, reserveErr := budget.Reserve(snapshot, need, now); !errors.Is(reserveErr, ErrCapacity) || release != nil {
			t.Fatalf("iteration %d overstated/understated capacity: result=%+v err=%v", iteration, result, reserveErr)
		}
		for _, release := range append(planned, held...) {
			release()
		}
	}
}

func TestCapacityPlanConcurrentWithReservations(t *testing.T) {
	budget, request := capacityFixture(t)
	var workers sync.WaitGroup
	for worker := range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				if worker%2 == 0 {
					result, err := budget.Plan(context.Background(), request)
					if err != nil || result.Validate() != nil {
						t.Errorf("invalid concurrent plan: %+v %v", result, err)
						return
					}
					continue
				}
				if release, err := budget.Reserve(request.Snapshot, request.Need, request.Now); err == nil {
					release()
				} else if !errors.Is(err, ErrCapacity) {
					t.Errorf("unexpected reservation error: %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()
}

func TestCapacityPlanPercentBoundariesMatchReserve(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, pool := range []string{"ram", "vram"} {
		for _, tc := range []struct {
			name      string
			available uint64
			wantAdmit bool
		}{{"below-ceiling", map[string]uint64{"ram": 201, "vram": 151}[pool], true}, {"at-ceiling", map[string]uint64{"ram": 200, "vram": 150}[pool], false}, {"above-ceiling", map[string]uint64{"ram": 199, "vram": 149}[pool], false}} {
			t.Run(pool+"/"+tc.name, func(t *testing.T) {
				budget, err := NewBudget(Limits{MaxConcurrent: 4, RAMPercent: 80, VRAMPercent: 85, MaxAge: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				need := Need{RAM: 1}
				snapshot := Snapshot{Time: now, CPUs: 4, TotalRAM: 1000, AvailableRAM: tc.available}
				if pool == "vram" {
					snapshot.AvailableRAM = 1000
					total, available := uint64(1000), tc.available
					snapshot.VRAMTotal, snapshot.VRAMAvailable = &total, &available
					need.VRAM = 1
				}
				result, err := budget.Plan(context.Background(), CapacityRequest{Version: 1, Snapshot: snapshot, Need: need, Now: now})
				if err != nil || result.Validate() != nil || (result.Action == CapacityAdmit) != tc.wantAdmit {
					t.Fatal(result, err)
				}
				release, reserveErr := budget.Reserve(snapshot, need, now)
				if tc.wantAdmit {
					if reserveErr != nil || release == nil {
						t.Fatal("plan admitted but reserve denied", reserveErr)
					}
					release()
				} else if !errors.Is(reserveErr, ErrCapacity) || release != nil {
					t.Fatal("plan waited but reserve admitted", reserveErr)
				}
			})
		}
	}
}
