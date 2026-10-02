package resources

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLinuxSparkUnifiedMemory(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, product string
		want                  bool
	}{
		{"spark", "NVIDIA\n", "NVIDIA_DGX_Spark\n", true},
		{"missing", "", "", false},
		{"discrete workstation", "NVIDIA", "DGX Station", false},
		{"wrong vendor", "Other", "NVIDIA_DGX_Spark", false},
		{"near match", "NVIDIA", "NVIDIA_DGX_Spark_fake", false},
		{"nul", "NVIDIA", "NVIDIA_DGX_Spark\x00", false},
		{"oversize", "NVIDIA", strings.Repeat(" ", 256) + "NVIDIA_DGX_Spark", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := cgroupProfileFixture()
			files["/sys/class/dmi/id/sys_vendor"] = tc.vendor
			files["/sys/class/dmi/id/product_name"] = tc.product
			setCgroupMemory(files, "/sys/fs/cgroup/parent/child", "524288", "131072", "max")
			s, err := profileLinuxHost(context.Background(), cgroupMapReader(files), func(context.Context) *bool { return nil })
			if err != nil || s.UnifiedMemory != tc.want || s.TotalRAM != 524288 || s.AvailableRAM != 393216 || s.VRAMTotal != nil || s.VRAMAvailable != nil {
				t.Fatalf("profile = %+v, %v", s, err)
			}
			if !tc.want {
				return
			}
			b, err := NewBudget(Limits{MaxConcurrent: 2, RAMPercent: 80, VRAMPercent: 85, MaxAge: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if release, err := b.Reserve(s, Need{RAM: 1, VRAM: 1}, time.Now()); err == nil || release != nil {
				t.Fatal("Spark admitted a separate VRAM pool")
			}
			release, err := b.Reserve(s, Need{RAM: 200000}, time.Now())
			if err != nil {
				t.Fatal("shared RAM reservation failed", err)
			}
			defer release()
			if extra, err := b.Reserve(s, Need{RAM: 200000}, time.Now()); err == nil || extra != nil {
				t.Fatal("shared RAM overcommitted")
			}
		})
	}
}

func TestLinuxUnifiedMemoryCanceledProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if linuxUnifiedMemory(ctx, func(context.Context, string) ([]byte, error) {
		t.Fatal("read after cancellation")
		return nil, nil
	}) {
		t.Fatal("canceled probe inferred unified memory")
	}
}
