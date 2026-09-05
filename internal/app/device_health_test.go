package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/telemetry"
	"darwinrouter/resources"
	"darwinrouter/runtime"
)

func TestHealthUsesConfiguredGPUInsteadOfAggregate(t *testing.T) {
	for _, mode := range []string{"healthy", "pressure", "missing", "stale", "unified"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := autoFixture(t)
			for i := range s.settings.Models {
				s.settings.Models[i].GPUDevice = "amd:card0"
				s.settings.Models[i].VRAMBytes = 1
			}
			s.profile = func(context.Context) (resources.Snapshot, error) {
				snapshot := deviceResourceSnapshot()
				total, free := uint64(10000), uint64(10000)
				snapshot.VRAMTotal, snapshot.VRAMAvailable = &total, &free
				switch mode {
				case "pressure":
					snapshot.GPUs.Sources[1].Devices[0].AvailableBytes = 0
				case "missing":
					snapshot.GPUs.Sources[1].Devices = snapshot.GPUs.Sources[1].Devices[1:]
				case "stale":
					snapshot.GPUs.Time = time.Now().Add(-time.Minute)
				case "unified":
					snapshot.UnifiedMemory = true
				}
				return snapshot, nil
			}
			db, err := telemetry.Open(context.Background(), cfg.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			report, err := s.HealthReport(context.Background(), healthySupervisor())
			if err != nil || report.Validate() != nil || report.Ready != (mode == "healthy") {
				t.Fatalf("%+v %v", report, err)
			}
		})
	}
}

func TestAutomaticRouteStoresSelectedGPUScalarsWithoutInventory(t *testing.T) {
	s, cfg := autoFixture(t)
	for i := range s.settings.Models {
		s.settings.Models[i].GPUDevice = "amd:card0"
		s.settings.Models[i].VRAMBytes = 1
	}
	s.profile = func(context.Context) (resources.Snapshot, error) { return deviceResourceSnapshot(), nil }
	out, err := s.Run(context.Background(), Request{ModelID: "auto", Prompt: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.OpenReadOnly(context.Background(), cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := db.Read(context.Background(), out.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind != runtime.RouteSelected {
			continue
		}
		found = true
		snapshot := event.Data.Resources
		if snapshot == nil || snapshot.GPUs != nil || snapshot.VRAMTotal == nil || *snapshot.VRAMTotal != 1000 || snapshot.VRAMAvailable == nil || *snapshot.VRAMAvailable != 1000 {
			t.Fatalf("%+v", snapshot)
		}
		body, _ := json.Marshal(event)
		if strings.Contains(string(body), "card0") || strings.Contains(string(body), "card1") || strings.Contains(string(body), "gpu_inventory") {
			t.Fatal("inventory escaped into journal", string(body))
		}
	}
	if !found {
		t.Fatal("no routing evidence")
	}
}
