package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func resourcePlanService(t *testing.T, mode, pressure string, profile func(context.Context) (resources.Snapshot, error)) *Service {
	t.Helper()
	cfg := config.Defaults()
	cfg.Mode = mode
	cfg.Hardware.Concurrent = "1"
	cfg.Hardware.LocalPressurePolicy = pressure
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "unused.db")
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.profile = profile
	return svc
}

func resourcePlanSnapshot() resources.Snapshot {
	return resources.Snapshot{Time: time.Now().UTC(), CPUs: 8, TotalRAM: 1000, AvailableRAM: 1000}
}

func TestResourcePlanMapsModePrivacyAndPressure(t *testing.T) {
	need := resources.Need{RAM: 100}
	for _, tc := range []struct {
		name, mode, pressure, action string
		localRequired                bool
		available                    uint64
	}{
		{"local-admit", "local_only", "reject", ResourceActionLocal, false, 1000},
		{"local-reject", "local_only", "reject", ResourceActionReject, false, 0},
		{"local-queue", "local_only", "wait", ResourceActionQueue, false, 0},
		{"hybrid-offload", "hybrid", "reject", ResourceActionOffload, false, 0},
		{"hybrid-private-reject", "hybrid", "reject", ResourceActionReject, true, 0},
		{"hybrid-wait-offload", "hybrid", "wait", ResourceActionOffload, false, 0},
		{"hybrid-private-wait", "hybrid", "wait", ResourceActionQueue, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := resourcePlanService(t, tc.mode, tc.pressure, func(context.Context) (resources.Snapshot, error) {
				snapshot := resourcePlanSnapshot()
				snapshot.AvailableRAM = tc.available
				return snapshot, nil
			})
			got, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: need, LocalRequired: tc.localRequired})
			if err != nil || got.Action != tc.action || got.Capacity.Validate() != nil {
				t.Fatalf("plan %+v err %v", got, err)
			}
		})
	}
}

func TestResourcePlanCloudOnlyDoesNotProfile(t *testing.T) {
	for _, localRequired := range []bool{false, true} {
		calls := 0
		svc := resourcePlanService(t, "cloud_only", "reject", func(context.Context) (resources.Snapshot, error) {
			calls++
			panic("profile must not run")
		})
		got, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: resources.Need{RAM: 1}, LocalRequired: localRequired})
		want := ResourceActionOffload
		if localRequired {
			want = ResourceActionReject
		}
		if err != nil || got.Action != want || calls != 0 || got.Capacity != (resources.CapacityResult{}) {
			t.Fatalf("plan %+v err %v calls %d", got, err, calls)
		}
	}
	calls := 0
	svc := resourcePlanService(t, "cloud_only", "reject", func(context.Context) (resources.Snapshot, error) {
		calls++
		return resourcePlanSnapshot(), nil
	})
	if out, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{}); !errors.Is(err, ErrAdmission) || out != (ResourcePlanResult{}) || calls != 0 {
		t.Fatalf("invalid cloud-only request: %+v err %v calls %d", out, err, calls)
	}
}

func TestResourcePlanObservesLiveReservationsWithoutMutation(t *testing.T) {
	svc := resourcePlanService(t, "local_only", "reject", func(context.Context) (resources.Snapshot, error) {
		return resourcePlanSnapshot(), nil
	})
	need := resources.Need{RAM: 100}
	settingsBefore, err := json.Marshal(svc.settings)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: need})
	second, secondErr := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: need})
	if err != nil || secondErr != nil || first.Action != ResourceActionLocal || second.Action != ResourceActionLocal || first.Capacity.MaxAdditional != 1 || second.Capacity.MaxAdditional != 1 {
		t.Fatal(first, err, second, secondErr)
	}
	first.Action = ResourceActionReject
	first.Capacity.Headroom.RAMBytes = 0
	release, err := svc.budget.Reserve(resourcePlanSnapshot(), need, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	held, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: need})
	if err != nil || held.Action != ResourceActionReject || held.Capacity.Action != resources.CapacityWait {
		t.Fatal(held, err)
	}
	release()
	recovered, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: need})
	if err != nil || recovered.Action != ResourceActionLocal {
		t.Fatal(recovered, err)
	}
	if _, err := os.Stat(svc.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resource planning touched durable storage", err)
	}
	settingsAfter, err := json.Marshal(svc.settings)
	if err != nil || string(settingsAfter) != string(settingsBefore) {
		t.Fatal("resource planning mutated configuration", err)
	}
}

func TestResourcePlanRejectsInvalidAndSanitizesFailure(t *testing.T) {
	svc := resourcePlanService(t, "local_only", "reject", func(context.Context) (resources.Snapshot, error) {
		panic("private profiler panic")
	})
	for _, request := range []ResourcePlanRequest{{}, {Need: resources.Need{RAM: 1, Device: "nvidia:0"}}, {Need: resources.Need{RAM: 1, VRAM: 1, Device: "invalid"}}} {
		if out, err := svc.ResourcePlan(context.Background(), request); !errors.Is(err, ErrAdmission) || out != (ResourcePlanResult{}) {
			t.Fatal(out, err)
		}
	}
	if out, err := svc.ResourcePlan(context.Background(), ResourcePlanRequest{Need: resources.Need{RAM: 1}}); err != ErrAdmission || out != (ResourcePlanResult{}) || errors.Is(err, resources.ErrProfile) {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := svc.ResourcePlan(ctx, ResourcePlanRequest{Need: resources.Need{RAM: 1}}); !errors.Is(err, ErrAdmission) || !errors.Is(err, context.Canceled) || out != (ResourcePlanResult{}) {
		t.Fatal(out, err)
	}
}
