package app

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/contextpolicy"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"math"
	"strings"
	"testing"
	"time"
)

func TestRequiredContextGrowthPreservesResidencyAdmission(t *testing.T) {
	svc, fixture := managedResidencyFixture(t)
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 131072
	}
	result, err := svc.Run(context.Background(), Request{Prompt: strings.Repeat("x", 40000)})
	if err != nil || result.Text != "answer" {
		t.Fatal("required larger tier skipped residency recovery", result, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.unloads != 1 || fixture.streams != 1 {
		t.Fatal(fixture.unloads, fixture.streams)
	}
}

func TestRequiredContextGrowthRetainsCapacityClassification(t *testing.T) {
	svc, _ := autoFixture(t)
	for i := range svc.settings.Models {
		svc.settings.Models[i].ContextTokens = 131072
	}
	release, err := svc.reserveExplicit(context.Background(), svc.settings.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result, err := svc.runAuto(context.Background(), Request{Prompt: strings.Repeat("x", 40000)})
	if result.TaskID != "" || !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("pressure cannot be retried", result, err)
	}
}

func TestContextReservationScalesMemoryWithoutDiscountOrOverflow(t *testing.T) {
	m := config.Model{Locality: "local", ContextTokens: 131072, RAMBytes: 100, VRAMBytes: 40}
	for _, tc := range []struct {
		tokens    int
		ram, vram uint64
	}{{16384, 100, 40}, {32768, 100, 40}, {65536, 200, 80}, {131072, 400, 160}} {
		got, err := contextReservationModel(m, tc.tokens)
		if err != nil || got.RAMBytes != tc.ram || got.VRAMBytes != tc.vram {
			t.Fatalf("%+v %v", got, err)
		}
	}
	m.RAMBytes = math.MaxUint64
	if _, err := contextReservationModel(m, 65536); err == nil {
		t.Fatal("overflow admitted")
	}
}

func TestContextExplorationRespectsBudgetAndPreservesResidencyAdmission(t *testing.T) {
	svc, _ := autoFixture(t)
	m := config.Model{Locality: "local", ContextTokens: 131072, RAMBytes: 100}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), TotalRAM: 200, AvailableRAM: 200}, nil
	}
	fit := svc.contextFitsMemory(context.Background(), m)
	evidence := []contextpolicy.Evidence{{ContextTokens: 32768, Samples: 2, Quality: 1}}
	tier, err := chooseContextTier(context.Background(), m, Request{}, 1024, evidence, true, 2, fit)
	if err != nil || tier != 32768 {
		t.Fatalf("failed optional exploration discarded baseline: %d %v", tier, err)
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, resources.ErrProfile }
	if !fit(32768) {
		t.Fatal("baseline denied before residency admission")
	}
	if fit(65536) {
		t.Fatal("explored without memory observation")
	}
}
