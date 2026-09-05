package app

import (
	"context"
	"os"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/resources"
)

func TestDelegateAdmissionPreservesLocalPrivacyAndReservations(t *testing.T) {
	for _, mode := range []string{"privacy", "reservation"} {
		t.Run(mode, func(t *testing.T) {
			fixture, cfg := autoFixture(t)
			cfg.Workers.DelegateModel = "z"
			cfg.Hardware.Concurrent = "1"
			if mode == "privacy" {
				cfg.Mode = "hybrid"
				cfg.Models[1].Locality = "cloud"
			}
			svc, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc.profile = fixture.profile
			if mode == "reservation" {
				release, err := svc.reserveExplicit(context.Background(), cfg.Models[0])
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			} else {
				svc.profile = func(context.Context) (resources.Snapshot, error) {
					t.Fatal("privacy denial reached profiling")
					return resources.Snapshot{}, nil
				}
			}
			result, err := svc.runDelegate(context.Background(), "private prompt", "", "work", true, "", "")
			if err == nil || result.TaskID != "" || result.Text != "" {
				t.Fatal("ineligible child admitted", result, err)
			}
			if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
				t.Fatal("child created storage before admission", err)
			}
			if len(svc.execution) != 0 {
				t.Fatal("child task slot leaked")
			}
		})
	}
}
