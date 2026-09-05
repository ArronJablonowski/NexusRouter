package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"darwinrouter/resources"
)

func TestAuxiliaryCallsShareBoundDeviceReservations(t *testing.T) {
	for _, operation := range []string{"audit", "summary"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			seed, cfg := autoFixture(t)
			source, err := seed.Run(ctx, Request{ModelID: "a", Prompt: "hello", Domain: "creative"})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				// A structurally valid provider response with an invalid generated
				// document proves admission succeeded while exercising error cleanup.
				fmt.Fprintln(w, `{"message":{"content":"not a valid review or summary"},"done":true,"done_reason":"stop"}`)
			}))
			defer provider.Close()
			cfg.Providers[0].Endpoint = provider.URL
			cfg.Hardware.Concurrent = "3"
			cfg.Models[1].GPUDevice = "amd:card0"
			cfg.Models[1].VRAMBytes = 600
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.profile = func(context.Context) (resources.Snapshot, error) { return deviceResourceSnapshot(), nil }
			invoke := func() error {
				if operation == "audit" {
					_, err := s.AuditTask(ctx, source.TaskID, "z", 0)
					return err
				}
				_, err := s.SummarizeTask(ctx, source.TaskID, "z", 1, 0)
				return err
			}
			hold, err := s.budget.Reserve(deviceResourceSnapshot(), resources.Need{RAM: 100, VRAM: 600, Device: "amd:card0"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer hold()
			if err := invoke(); !errors.Is(err, ErrAdmission) || calls.Load() != 0 {
				t.Fatal("occupied bound GPU admitted auxiliary work", err, calls.Load())
			}
			// A second device is available with the same source/model/operation,
			// ruling out a denial caused by unrelated admission prerequisites.
			s.settings.Models[1].GPUDevice = "amd:card1"
			if err := invoke(); err == nil || errors.Is(err, ErrAdmission) || calls.Load() != 1 {
				t.Fatal("unoccupied GPU failed admission", err, calls.Load())
			}
			s.settings.Models[1].GPUDevice = "amd:card0"
			if err := invoke(); !errors.Is(err, ErrAdmission) || calls.Load() != 1 {
				t.Fatal("other-device call released held GPU", err, calls.Load())
			}
			hold()
			if err := invoke(); err == nil || errors.Is(err, ErrAdmission) || calls.Load() != 2 {
				t.Fatal("released GPU failed admission", err, calls.Load())
			}
			// Malformed output must still release the auxiliary device reservation.
			for _, device := range []string{"amd:card0", "amd:card1"} {
				release, err := s.budget.Reserve(deviceResourceSnapshot(), resources.Need{RAM: 100, VRAM: 600, Device: device}, time.Now())
				if err != nil {
					t.Fatal("auxiliary error leaked reservation", device, err)
				}
				release()
			}
		})
	}
}
