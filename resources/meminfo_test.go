package resources

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestParseLinuxMeminfo(t *testing.T) {
	for _, tc := range []struct {
		name, body       string
		total, available uint64
		swap             *uint64
	}{
		{"minimum", "MemTotal: 1 kB\nMemAvailable: 0 kB\n", 1024, 0, nil},
		{"unknown ignored", "MemTotal: 1024 kB\nMemAvailable: 512 kB\nHugePages_Total: 0\nIgnored: unknown data\nIgnored: repeated\n", 1 << 20, 512 << 10, nil},
		{"swap", "MemTotal: 1024 kB\nMemAvailable: 512 kB\nSwapTotal: 128 kB\nSwapFree: 32 kB\n", 1 << 20, 512 << 10, meminfoUint(96 << 10)},
		{"zero swap", "MemTotal: 1 kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB", 1024, 1024, meminfoUint(0)},
		{"extreme", fmt.Sprintf("MemTotal: %d kB\nMemAvailable: %d kB", uint64(math.MaxUint64/1024), uint64(math.MaxUint64/1024)), uint64(math.MaxUint64/1024) * 1024, uint64(math.MaxUint64/1024) * 1024, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, available, swap, err := parseLinuxMeminfo([]byte(tc.body))
			if err != nil || total != tc.total || available != tc.available || (swap == nil) != (tc.swap == nil) || (swap != nil && *swap != *tc.swap) {
				t.Fatal(total, available, swap, err)
			}
		})
	}
}
func meminfoUint(n uint64) *uint64 { return &n }

func TestParseLinuxMeminfoRejectsMalformedRelevantData(t *testing.T) {
	base := "MemTotal: 1024 kB\nMemAvailable: 512 kB\n"
	for _, body := range []string{"", "MemTotal: 1 kB", "MemAvailable: 0 kB", "MemTotal: 0 kB\nMemAvailable: 0 kB", "MemTotal: 1 kB\nMemAvailable: 2 kB", base + "SwapTotal: 1 kB", base + "SwapFree: 0 kB", base + "SwapTotal: 1 kB\nSwapFree: 2 kB", base + "MemTotal: 1024 kB", base + "MemAvailable: 512 kB", base + "SwapTotal: 1 kB\nSwapFree: 0 kB\nSwapFree: 0 kB", strings.Repeat("x", 65537), "MemTotal 1024 kB\nMemAvailable: 0 kB"} {
		assertBadMeminfo(t, body)
	}
	for _, value := range []string{"-1", "+1", "1.5", "NaN", "0x10", "18446744073709551616", fmt.Sprint(uint64(math.MaxUint64/1024) + 1)} {
		assertBadMeminfo(t, "MemTotal: "+value+" kB\nMemAvailable: 0 kB")
	}
	for _, suffix := range []string{"1 MB", "1", "1 kB extra", "1 KB", ": 1 kB", "1kB"} {
		assertBadMeminfo(t, "MemTotal: "+suffix+"\nMemAvailable: 0 kB")
	}
}

func TestParseLinuxMeminfoExactByteBound(t *testing.T) {
	base := "MemTotal: 1 kB\nMemAvailable: 0 kB\nIgnored: "
	body := base + strings.Repeat("x", (64<<10)-len(base))
	if total, _, _, err := parseLinuxMeminfo([]byte(body)); err != nil || total != 1024 {
		t.Fatal(total, err)
	}
	assertBadMeminfo(t, body+"x")
}
func assertBadMeminfo(t *testing.T, body string) {
	t.Helper()
	total, available, swap, err := parseLinuxMeminfo([]byte(body))
	if !errors.Is(err, ErrProfile) || total != 0 || available != 0 || swap != nil {
		t.Fatalf("partial or invalid accepted: %d %d %v %v", total, available, swap, err)
	}
}
