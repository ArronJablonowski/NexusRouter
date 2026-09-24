package app

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/contextpolicy"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"math"
	"testing"
	"time"
)

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
