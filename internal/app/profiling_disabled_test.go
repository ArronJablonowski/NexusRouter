package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestDisabledProfilingDeniesLocalWithoutStorage(t *testing.T) {
	_, cfg := autoFixture(t)
	cfg.Hardware.AutoProfile = false
	for _, concurrent := range []string{"auto", "4"} {
		cfg.Hardware.Concurrent = concurrent
		s, err := NewService(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot, err := s.profile(context.Background()); !errors.Is(err, resources.ErrProfile) || !snapshot.Time.IsZero() || snapshot.TotalRAM != 0 {
			t.Fatal("disabled profile fabricated capacity", snapshot, err)
		}
		if _, err := s.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"}); !errors.Is(err, ErrAdmission) {
			t.Fatal("disabled profiler admitted explicit local execution", err)
		}
		if _, err := os.Stat(cfg.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("denied explicit execution created storage", err)
		}
	}
}

func TestDisabledProfilingStillPermitsEligibleCloudRoutes(t *testing.T) {
	_, cfg := autoFixture(t)
	cfg.Hardware.AutoProfile = false
	cfg.Mode = "hybrid"
	cfg.Models[1].Locality = "cloud"
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"z", "auto"} {
		out, err := s.Run(context.Background(), Request{ModelID: model, Prompt: "hello"})
		if err != nil || out.Text != "z" {
			t.Fatal("eligible cloud route blocked or local route selected", model, out, err)
		}
	}
}
