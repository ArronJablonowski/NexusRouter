package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"darwinrouter/resources"
)

func deviceResourceSnapshot() resources.Snapshot {
	now := time.Now().UTC()
	return resources.Snapshot{Time: now, CPUs: 8, TotalRAM: 1000, AvailableRAM: 1000, GPUs: &resources.GPUInventory{Time: now, Sources: []resources.GPUObservation{
		{Source: "nvidia-smi", Status: "unavailable", Devices: []resources.GPUDevice{}},
		{Source: "amdgpu-sysfs", Status: "observed", Devices: []resources.GPUDevice{
			{ID: "card0", Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: 1000, AvailableBytes: 1000},
			{ID: "card1", Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: 1000, AvailableBytes: 1000},
		}},
	}}}
}

func TestDeviceResourcesSharedServiceExplicitAndAutomatic(t *testing.T) {
	for _, mode := range []string{"explicit", "automatic", "host_ram"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, cfg := autoFixture(t)
			cfg.Hardware.Concurrent = "3"
			cfg.Workers.Max = 3
			cfg.Models[0].GPUDevice = "amd:card0"
			cfg.Models[1].GPUDevice = "amd:card1"
			for i := range cfg.Models {
				cfg.Models[i].VRAMBytes = 600
				if mode == "host_ram" {
					cfg.Models[i].RAMBytes = 500
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				var body struct {
					Model string `json:"model"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad provider request")
					return
				}
				if calls.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					case <-ctx.Done():
						return
					}
				}
				fmt.Fprintf(w, "{\"message\":{\"content\":%q},\"done\":true,\"done_reason\":\"stop\"}\n", body.Model)
			}))
			defer func() { cancel(); provider.Close() }()
			cfg.Providers[0].Endpoint = provider.URL
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.profile = func(context.Context) (resources.Snapshot, error) { return deviceResourceSnapshot(), nil }
			done := make(chan error, 1)
			go func() { _, err := s.Run(ctx, Request{ModelID: "a", Prompt: "hold GPU A"}); done <- err }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatal("first binding denied", err)
			case <-ctx.Done():
				t.Fatal("GPU A did not dispatch")
			}
			out, err := s.Run(ctx, Request{ModelID: "a", Prompt: "GPU A already reserved"})
			if !errors.Is(err, ErrAdmission) || out.TaskID != "" || calls.Load() != 1 {
				t.Fatal("same GPU overbooked", out, err, calls.Load())
			}
			model := "z"
			if mode == "automatic" {
				model = "auto"
			}
			out, err = s.Run(ctx, Request{ModelID: model, Prompt: "use GPU B"})
			if mode == "host_ram" {
				if !errors.Is(err, ErrAdmission) || out.TaskID != "" || calls.Load() != 1 {
					t.Fatal("host RAM escaped", out, err)
				}
			} else if err != nil || out.Text != "z" || calls.Load() != 2 {
				t.Fatal("independent GPU B denied", out, err, calls.Load())
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("held task did not finish")
			}
			if _, err := s.Run(ctx, Request{ModelID: "a", Prompt: "GPU A released"}); err != nil {
				t.Fatal("device reservation leaked", err)
			}
		})
	}
}

func TestDeviceResourcesUnknownStaleAndWrongDeviceFailBeforeDispatch(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, mode := range []string{"unknown", "stale", "missing_inventory", "unavailable_source", "wrong_capacity"} {
			t.Run(fmt.Sprintf("%t/%s", automatic, mode), func(t *testing.T) {
				_, cfg := autoFixture(t)
				cfg.Models = cfg.Models[:1]
				cfg.Hardware.Concurrent = "2"
				cfg.Models[0].GPUDevice = "amd:card0"
				cfg.Models[0].VRAMBytes = 600
				if mode == "unknown" {
					cfg.Models[0].GPUDevice = "amd:card7"
				}
				s, err := NewService(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				s.profile = func(context.Context) (resources.Snapshot, error) {
					snapshot := deviceResourceSnapshot()
					// Aggregate legacy capacity must never override an explicit binding.
					huge := uint64(1 << 40)
					snapshot.VRAMTotal = &huge
					snapshot.VRAMAvailable = &huge
					switch mode {
					case "stale":
						snapshot.GPUs.Time = time.Now().Add(-time.Minute)
					case "missing_inventory":
						snapshot.GPUs = nil
					case "unavailable_source":
						snapshot.GPUs.Sources[1].Status = "unavailable"
						snapshot.GPUs.Sources[1].Devices = nil
					case "wrong_capacity":
						snapshot.GPUs.Sources[1].Devices[0].AvailableBytes = 1
						snapshot.GPUs.Sources[1].Devices[1].TotalBytes = huge
						snapshot.GPUs.Sources[1].Devices[1].AvailableBytes = huge
					}
					return snapshot, nil
				}
				model := "a"
				if automatic {
					model = "auto"
				}
				out, err := s.Run(context.Background(), Request{ModelID: model, Prompt: "hello"})
				if !errors.Is(err, ErrAdmission) || out.TaskID != "" {
					t.Fatal("invalid binding dispatched", out, err)
				}
			})
		}
	}
}
