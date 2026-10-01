package app

import (
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestNativeReservationIncludesFixedHarnessOverhead(t *testing.T) {
	model := config.Model{ID: "fixture", Locality: "local", RAMBytes: 1000, VRAMBytes: 100, ContextTokens: 32768, DefaultContextTokens: 8192}
	entry := NativeHarness{OverheadRAMBytes: 500}
	for _, tc := range []struct {
		tokens            int
		wantRAM, wantVRAM uint64
	}{{4096, 1500, 100}, {8192, 1500, 100}, {16384, 2500, 200}} {
		got, err := nativeReservationModel(model, &entry, tc.tokens)
		if err != nil || got.RAMBytes != tc.wantRAM || got.VRAMBytes != tc.wantVRAM {
			t.Fatal(got, err)
		}
		resized, err := contextReservationModel(got, tc.tokens)
		if err != nil || resized.RAMBytes != got.RAMBytes {
			t.Fatal("overhead/context sized twice", resized, err)
		}
	}
	model.Locality = "cloud"
	got, err := nativeReservationModel(model, &entry, 8192)
	if err != nil || got.Locality != "local" || got.RAMBytes != 500 || got.VRAMBytes != 0 {
		t.Fatal("cloud model omitted local harness process reservation", got, err)
	}
}
