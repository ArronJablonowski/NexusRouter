package resources

import (
	"errors"
	"math"
	"testing"
	"time"
)

func deviceSnapshot(now time.Time) Snapshot {
	return Snapshot{Time: now, CPUs: 64, TotalRAM: 128 << 30, AvailableRAM: 128 << 30, GPUs: &GPUInventory{Time: now, Sources: []GPUObservation{{Source: "nvidia-smi", Status: "observed", Devices: []GPUDevice{{ID: "GPU-abcdef01", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: 100, AvailableBytes: 100}, {ID: "GPU-abcdef02", Vendor: "nvidia", Source: "nvidia-smi", TotalBytes: 100, AvailableBytes: 100}}}}}}
}

func TestDeviceBudgetIndependentPoolsAndAliases(t *testing.T) {
	now := time.Now()
	s := deviceSnapshot(now)
	b, _ := NewBudget(Limits{8, 100, 100, time.Second})
	first, err := b.Reserve(s, Need{RAM: 1, VRAM: 80, Device: "nvidia:GPU-abcdef01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Reserve(s, Need{RAM: 1, VRAM: 30, Device: "nvidia:GPU-ABCDEF01"}, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("alias bypass", err)
	}
	second, err := b.Reserve(s, Need{RAM: 1, VRAM: 80, Device: "nvidia:GPU-abcdef02"}, now)
	if err != nil {
		t.Fatal("independent device denied", err)
	}
	first()
	first()
	second()
	if len(b.deviceVRAM) != 0 || b.active != 0 || b.used.RAM != 0 {
		t.Fatal("release leaked")
	}
}

func TestDeviceBudgetRejectsMixingWhileRAMOnlyUnaffected(t *testing.T) {
	now := time.Now()
	s := deviceSnapshot(now)
	total := uint64(100)
	s.VRAMTotal = &total
	s.VRAMAvailable = &total
	for _, boundFirst := range []bool{true, false} {
		b, _ := NewBudget(Limits{8, 100, 100, time.Second})
		bound := Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-abcdef01"}
		aggregate := Need{RAM: 1, VRAM: 1}
		first, next := bound, aggregate
		if !boundFirst {
			first, next = aggregate, bound
		}
		release, err := b.Reserve(s, first, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.Reserve(s, next, now); !errors.Is(err, ErrCapacity) {
			t.Fatal("mix accepted", err)
		}
		ram, err := b.Reserve(s, Need{RAM: 1}, now)
		if err != nil {
			t.Fatal(err)
		}
		ram()
		release()
		releaseNext, err := b.Reserve(s, next, now)
		if err != nil {
			t.Fatal("released overlap retained", err)
		}
		releaseNext()
	}
}

func TestDeviceMemoryRejectsInvalidInventories(t *testing.T) {
	now := time.Now()
	for _, mode := range []string{"missing", "stale", "future", "duplicate_source", "duplicate_device", "wrong_source", "wrong_vendor", "invalid_capacity", "unavailable", "unified", "missing_device"} {
		t.Run(mode, func(t *testing.T) {
			s := deviceSnapshot(now)
			id := "nvidia:GPU-abcdef01"
			switch mode {
			case "missing":
				s.GPUs = nil
			case "stale":
				s.GPUs.Time = now.Add(-2 * time.Second)
			case "future":
				s.GPUs.Time = now.Add(time.Second)
			case "duplicate_source":
				s.GPUs.Sources = append(s.GPUs.Sources, s.GPUs.Sources[0])
			case "duplicate_device":
				s.GPUs.Sources[0].Devices[1] = s.GPUs.Sources[0].Devices[0]
			case "wrong_source":
				s.GPUs.Sources[0].Devices[0].Source = "amdgpu-sysfs"
			case "wrong_vendor":
				s.GPUs.Sources[0].Devices[0].Vendor = "amd"
			case "invalid_capacity":
				s.GPUs.Sources[0].Devices[0].AvailableBytes = 101
			case "unavailable":
				s.GPUs.Sources[0].Status = "unavailable"
			case "unified":
				s.UnifiedMemory = true
			case "missing_device":
				id = "amd:card0"
			}
			if _, _, err := DeviceMemory(s, id, now, time.Second); !errors.Is(err, ErrResourceData) {
				t.Fatal(err)
			}
		})
	}
	for _, id := range []string{"amd:card01", "amd:card-1", "amd:card4294967296", "nvidia:GPU-../../etc", "card0", "amd:card+1"} {
		if ValidGPUDeviceID(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"amd:card0", "amd:card4294967295", "nvidia:GPU-deadbeef"} {
		if !ValidGPUDeviceID(id) {
			t.Fatal(id)
		}
	}
}

func TestDeviceReservationsDoNotSumOverflow(t *testing.T) {
	now := time.Now()
	s := deviceSnapshot(now)
	for i := range s.GPUs.Sources[0].Devices {
		s.GPUs.Sources[0].Devices[i].TotalBytes = math.MaxUint64
		s.GPUs.Sources[0].Devices[i].AvailableBytes = math.MaxUint64
	}
	b, _ := NewBudget(Limits{8, 100, 100, time.Second})
	for _, id := range []string{"nvidia:GPU-abcdef01", "nvidia:GPU-abcdef02"} {
		release, err := b.Reserve(s, Need{RAM: 1, VRAM: math.MaxUint64, Device: id}, now)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	if b.used.VRAM != 0 || len(b.deviceVRAM) != 2 {
		t.Fatal("device pools summed")
	}
}

func TestAdaptiveBudgetUsesBoundDeviceHeadroom(t *testing.T) {
	now := time.Now()
	s := deviceSnapshot(now)
	for i := range s.GPUs.Sources[0].Devices {
		s.GPUs.Sources[0].Devices[i].TotalBytes = 128 << 30
		s.GPUs.Sources[0].Devices[i].AvailableBytes = 128 << 30
	}
	s.GPUs.Sources[0].Devices[0].AvailableBytes = 8 << 30
	b, _ := NewAdaptiveBudget(Limits{8, 100, 100, time.Second})
	ram, err := b.Reserve(s, Need{RAM: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer ram()
	small, err := b.Reserve(s, Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-abcdef01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer small()
	if _, err = b.Reserve(s, Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-ABCDEF01"}, now); !errors.Is(err, ErrCapacity) {
		t.Fatal("same device tier bypass", err)
	}
	release, err := b.Reserve(s, Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-abcdef02"}, now)
	if err != nil {
		t.Fatal("unrelated small device constrained admission", err)
	}
	release()
	if _, err = b.Reserve(s, Need{RAM: 1, Device: "nvidia:GPU-abcdef02"}, now); !errors.Is(err, ErrResourceData) {
		t.Fatal("device without VRAM accepted", err)
	}
}

func TestAdaptiveIndependentSmallDevicesAndGlobalCaps(t *testing.T) {
	for _, mode := range []string{"independent", "cpu", "ram", "configured"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			s := deviceSnapshot(now)
			for i := range s.GPUs.Sources[0].Devices {
				s.GPUs.Sources[0].Devices[i].TotalBytes = 8 << 30
				s.GPUs.Sources[0].Devices[i].AvailableBytes = 8 << 30
			}
			cap := 8
			switch mode {
			case "cpu":
				s.CPUs = 1
			case "ram":
				s.TotalRAM = 8 << 30
				s.AvailableRAM = 8 << 30
			case "configured":
				cap = 1
			}
			b, _ := NewAdaptiveBudget(Limits{cap, 100, 100, time.Second})
			first, err := b.Reserve(s, Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-abcdef01"}, now)
			if err != nil {
				t.Fatal(err)
			}
			second, err := b.Reserve(s, Need{RAM: 1, VRAM: 1, Device: "nvidia:GPU-abcdef02"}, now)
			if mode == "independent" {
				if err != nil {
					t.Fatal("independent small GPU blocked", err)
				}
				second()
			} else if !errors.Is(err, ErrCapacity) {
				t.Fatal("global cap bypass", err)
			}
			first()
			first()
			if len(b.deviceActive) != 0 || len(b.deviceVRAM) != 0 {
				t.Fatal("device bookkeeping leaked")
			}
		})
	}
}
