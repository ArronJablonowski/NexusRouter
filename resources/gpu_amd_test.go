package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func amdFixture(t *testing.T, root, card, vendor, total, used string) {
	t.Helper()
	base := filepath.Join(root, card, "device")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"vendor": vendor, "mem_info_vram_total": total, "mem_info_vram_used": used} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAMDProbeDevicesAndSysfsLinks(t *testing.T) {
	root := t.TempDir()
	amdFixture(t, root, "card0", "0x1002\n", "1000\n", "200\n")
	amdFixture(t, root, "card2", "0x10de\n", "bad", "bad")
	physical := t.TempDir()
	amdFixture(t, physical, "card1", "0x1002\n", "18446744073709551615\n", "0\n")
	if err := os.Symlink(filepath.Join(physical, "card1"), filepath.Join(root, "card1")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"card0-HDMI-A-1", "renderD128", "card01", "card-1", "card+3", "card4294967296", "card", "version"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := probeAMDGPUs(context.Background(), root)
	want := []GPUDevice{{ID: "card0", Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: 1000, AvailableBytes: 800}, {ID: "card1", Vendor: "amd", Source: "amdgpu-sysfs", TotalBytes: ^uint64(0), AvailableBytes: ^uint64(0)}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}

func TestAMDProbeInvalidMetricsHaveNoPartialResults(t *testing.T) {
	for _, tc := range []struct{ total, used string }{
		{"0", "0"}, {"1", "2"}, {"1", "-1"}, {"1", "+1"}, {"1", "1.0"}, {"1", "0x0"}, {"1", " 0"}, {"1", "0\n\n"}, {"1", ""}, {"1", "18446744073709551616"}, {"18446744073709551616", "0"}, {strings.Repeat("1", 65), "0"}, {"1", "０"},
	} {
		t.Run(fmt.Sprintf("%q/%q", tc.total, tc.used), func(t *testing.T) {
			root := t.TempDir()
			amdFixture(t, root, "card0", "0x1002", "100", "0")
			amdFixture(t, root, "card1", "0x1002", tc.total, tc.used)
			got, err := probeAMDGPUs(context.Background(), root)
			if !errors.Is(err, ErrProfile) || got != nil {
				t.Fatal(got, err)
			}
		})
	}
}

func TestAMDProbeVendorValidation(t *testing.T) {
	for _, vendor := range []string{"", "unknown", "1002", "0X1002", "0x100", "0x10022", "0x10gg", "0x+123", "0x1002\n\n", " 0x1002"} {
		root := t.TempDir()
		amdFixture(t, root, "card0", "0x1002", "100", "0")
		amdFixture(t, root, "card1", vendor, "100", "0")
		if got, err := probeAMDGPUs(context.Background(), root); !errors.Is(err, ErrProfile) || got != nil {
			t.Fatal(vendor, got, err)
		}
	}
	for _, vendor := range []string{"0x10de", "0x10DE\n", "0xffff", "0x1234"} {
		root := t.TempDir()
		amdFixture(t, root, "card0", vendor, "bad", "bad")
		if got, err := probeAMDGPUs(context.Background(), root); err != nil || len(got) != 0 {
			t.Fatal(vendor, got, err)
		}
	}
}

func TestAMDProbeMissingFilesAndBounds(t *testing.T) {
	for _, name := range []string{"vendor", "mem_info_vram_total", "mem_info_vram_used"} {
		root := t.TempDir()
		amdFixture(t, root, "card0", "0x1002", "100", "0")
		if err := os.Remove(filepath.Join(root, "card0", "device", name)); err != nil {
			t.Fatal(err)
		}
		if got, err := probeAMDGPUs(context.Background(), root); !errors.Is(err, ErrProfile) || got != nil {
			t.Fatal(got, err)
		}
	}
	for _, count := range []int{32, 33} {
		root := t.TempDir()
		for i := 0; i < count; i++ {
			amdFixture(t, root, fmt.Sprint("card", i), "0x1002", "1", "1")
		}
		got, err := probeAMDGPUs(context.Background(), root)
		if count == 32 && (err != nil || len(got) != 32) || count == 33 && (!errors.Is(err, ErrProfile) || got != nil) {
			t.Fatal(count, got, err)
		}
	}
	root := t.TempDir()
	for i := 0; i < 257; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint("connector", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := probeAMDGPUs(context.Background(), root); !errors.Is(err, ErrProfile) || got != nil {
		t.Fatal(got, err)
	}
}

func TestAMDProbeMissingRootCancellationAndEmptyDiscovery(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if got, err := probeAMDGPUs(context.Background(), missing); !errors.Is(err, ErrProfile) || got != nil {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe created missing root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := probeAMDGPUs(ctx, root); !errors.Is(err, ErrProfile) || got != nil {
		t.Fatal(got, err)
	}
	got, err := probeAMDGPUs(context.Background(), root)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
