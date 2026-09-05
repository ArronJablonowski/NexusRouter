package resources

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Every input is supplied through this map: these tests never inspect the
// executing machine's procfs or cgroup membership.
func cgroupProfileFixture() map[string]string {
	return map[string]string{
		"/proc/meminfo":                                  "MemTotal: 1024 kB\nMemAvailable: 768 kB\nSwapTotal: 128 kB\nSwapFree: 64 kB\n",
		"/proc/self/cgroup":                              "0::/parent/child\n",
		"/proc/self/mountinfo":                           "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/cgroup.controllers":              "",
		"/sys/fs/cgroup/parent/cgroup.controllers":       "",
		"/sys/fs/cgroup/parent/child/cgroup.controllers": "",
	}
}

func cgroupMapReader(files map[string]string) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, name string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, ok := files[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(body), nil
	}
}

func setCgroupMemory(files map[string]string, path, limit, current, high string) {
	files[path+"/memory.max"] = limit + "\n"
	files[path+"/memory.current"] = current + "\n"
	files[path+"/memory.high"] = high + "\n"
}

func TestLinuxCgroupProfileEffectiveHeadroom(t *testing.T) {
	for _, tc := range []struct {
		name, childMax, childCurrent, childHigh, parentMax, parentCurrent, parentHigh string
		total, available                                                              uint64
	}{
		{"child limit", "524288", "131072", "max", "max", "0", "max", 524288, 393216},
		{"ancestor siblings", "524288", "131072", "max", "786432", "720896", "max", 524288, 65536},
		{"ancestor tighter", "max", "131072", "max", "262144", "196608", "max", 262144, 65536},
		{"high pressure", "524288", "196608", "262144", "max", "0", "max", 262144, 65536},
		{"over limit", "262144", "327680", "max", "max", "0", "max", 262144, 0},
		{"unlimited", "max", "9999999", "max", "max", "9999999", "max", 1048576, 786432},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := cgroupProfileFixture()
			setCgroupMemory(files, "/sys/fs/cgroup/parent/child", tc.childMax, tc.childCurrent, tc.childHigh)
			setCgroupMemory(files, "/sys/fs/cgroup/parent", tc.parentMax, tc.parentCurrent, tc.parentHigh)
			s, err := profileLinux(context.Background(), cgroupMapReader(files))
			if err != nil || s.TotalRAM != tc.total || s.AvailableRAM != tc.available {
				t.Fatalf("got %+v, %v; want total=%d available=%d", s, err, tc.total, tc.available)
			}
			if s.Source != "linux-proc-cgroup-v2" || s.SwapUsed == nil || *s.SwapUsed != 65536 || s.CPUs < 1 || s.Time.IsZero() {
				t.Fatalf("invalid metadata: %+v", s)
			}
		})
	}
}

func TestLinuxCgroupProfileMissingControllersAndHostFallback(t *testing.T) {
	for _, tc := range []struct{ name, membership, source string }{
		{"missing controllers", "0::/parent/child\n", "linux-proc-cgroup-v2"},
		{"unified root", "0::/\n", "linux-proc-cgroup-v2"},
		{"legacy unrelated", "5:devices:/sandbox\n", "linux-proc-meminfo-host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := cgroupProfileFixture()
			files["/proc/self/cgroup"] = tc.membership
			s, err := profileLinux(context.Background(), cgroupMapReader(files))
			if err != nil || s.TotalRAM != 1048576 || s.AvailableRAM != 786432 || s.Source != tc.source {
				t.Fatalf("got %+v, %v", s, err)
			}
		})
	}
}

func TestLinuxCgroupProfileRejectsUnsafeInputs(t *testing.T) {
	for _, tc := range []struct{ name, path, value string }{
		{"partial memory", "/sys/fs/cgroup/parent/child/memory.max", "1024\n"},
		{"negative current", "/sys/fs/cgroup/parent/child/memory.current", "-1\n"},
		{"malformed max", "/sys/fs/cgroup/parent/child/memory.max", "unlimited\n"},
		{"malformed high", "/sys/fs/cgroup/parent/child/memory.high", "1.5\n"},
		{"malformed cpu", "/sys/fs/cgroup/parent/child/cpu.max", "oops\n"},
		{"malformed cpuset", "/sys/fs/cgroup/parent/child/cpuset.cpus.effective", "3-1\n"},
		{"legacy memory", "/proc/self/cgroup", "5:cpu,memory:/sandbox\n"},
		{"hybrid memory", "/proc/self/cgroup", "0::/parent/child\n5:memory:/sandbox\n"},
		{"invalid meminfo", "/proc/meminfo", "MemTotal: 1024 kB\n"},
		{"malformed membership", "/proc/self/cgroup", "not-a-membership\n"},
		{"unmounted group", "/proc/self/mountinfo", "29 23 0:26 / /tmp rw - tmpfs tmpfs rw\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := cgroupProfileFixture()
			if tc.name != "partial memory" {
				setCgroupMemory(files, "/sys/fs/cgroup/parent/child", "524288", "131072", "max")
			}
			files[tc.path] = tc.value
			if _, err := profileLinux(context.Background(), cgroupMapReader(files)); !errors.Is(err, ErrProfile) {
				t.Fatalf("unsafe input accepted: %v", err)
			}
		})
	}
	for _, path := range []string{"/proc/meminfo", "/proc/self/cgroup", "/proc/self/mountinfo", "/sys/fs/cgroup/parent/memory.max", "/sys/fs/cgroup/parent/cpu.max", "/sys/fs/cgroup/parent/cpuset.cpus.effective"} {
		t.Run("unreadable "+path, func(t *testing.T) {
			read := cgroupMapReader(cgroupProfileFixture())
			_, err := profileLinux(context.Background(), func(ctx context.Context, name string) ([]byte, error) {
				if name == path {
					return nil, os.ErrPermission
				}
				return read(ctx, name)
			})
			if !errors.Is(err, ErrProfile) {
				t.Fatalf("unreadable input accepted: %v", err)
			}
		})
	}
}

func TestLinuxCgroupProfileRejectsMovement(t *testing.T) {
	for _, path := range []string{"/proc/self/cgroup", "/proc/self/mountinfo"} {
		t.Run(path, func(t *testing.T) {
			read := cgroupMapReader(cgroupProfileFixture())
			calls := 0
			_, err := profileLinux(context.Background(), func(ctx context.Context, name string) ([]byte, error) {
				body, err := read(ctx, name)
				if name == path {
					calls++
					if calls > 1 {
						body = append(body, '\n')
					}
				}
				return body, err
			})
			if calls < 2 || !errors.Is(err, ErrProfile) {
				t.Fatalf("movement accepted: reads=%d error=%v", calls, err)
			}
		})
	}
}

func TestLinuxCgroupProfileCancellationAndDepth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := profileLinux(ctx, cgroupMapReader(cgroupProfileFixture())); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	files := cgroupProfileFixture()
	files["/proc/self/cgroup"] = "0::/" + strings.Repeat("group/", 65) + "leaf\n"
	if _, err := profileLinux(context.Background(), cgroupMapReader(files)); !errors.Is(err, ErrProfile) {
		t.Fatalf("unbounded ancestry accepted: %v", err)
	}
}

func TestLinuxCgroupProfileRequiresLiveDirectory(t *testing.T) {
	for _, path := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/parent", "/sys/fs/cgroup/parent/child"} {
		t.Run(path, func(t *testing.T) {
			files := cgroupProfileFixture()
			delete(files, path+"/cgroup.controllers")
			if _, err := profileLinux(context.Background(), cgroupMapReader(files)); !errors.Is(err, ErrProfile) {
				t.Fatalf("missing cgroup directory accepted: %v", err)
			}
		})
	}
}

func TestLinuxCgroupProfileMountRootAndCPU(t *testing.T) {
	files := cgroupProfileFixture()
	// A bind-mounted hierarchy exposes /tenant as its visible root. The
	// namespace-relative path must not be appended twice or escape the mount.
	files["/proc/self/cgroup"] = "0::/tenant/child\n"
	files["/proc/self/mountinfo"] = "29 23 0:26 /tenant /sys/fs/cgroup rw - cgroup2 cgroup rw\n"
	files["/sys/fs/cgroup/child/cgroup.controllers"] = ""
	setCgroupMemory(files, "/sys/fs/cgroup/child", "524288", "131072", "max")
	setCgroupMemory(files, "/sys/fs/cgroup", "786432", "720896", "max")
	files["/sys/fs/cgroup/child/cpu.max"] = "200000 100000\n"
	files["/sys/fs/cgroup/cpu.max"] = "100000 100000\n"
	files["/sys/fs/cgroup/child/cpuset.cpus.effective"] = "0-3,8\n"
	seen := map[string]bool{}
	read := cgroupMapReader(files)
	s, err := profileLinux(context.Background(), func(ctx context.Context, name string) ([]byte, error) {
		seen[name] = true
		return read(ctx, name)
	})
	if err != nil || s.TotalRAM != 524288 || s.AvailableRAM != 65536 || s.CPUs != 1 {
		t.Fatalf("visible ancestor limits not applied: %+v %v", s, err)
	}
	if seen["/sys/fs/cgroup/tenant/child/memory.max"] || seen["/sys/fs/cgroup/../memory.max"] {
		t.Fatal("probed outside resolved hierarchy")
	}
}

func TestLinuxCgroupProfileDrivesAdaptiveAdmission(t *testing.T) {
	files := cgroupProfileFixture()
	files["/proc/meminfo"] = "MemTotal: 134217728 kB\nMemAvailable: 134217728 kB\n"
	setCgroupMemory(files, "/sys/fs/cgroup/parent/child", "103079215104", "8589934592", "max")
	files["/sys/fs/cgroup/parent/cpu.max"] = "100000 100000\n"
	s, err := profileLinux(context.Background(), cgroupMapReader(files))
	if err != nil || s.TotalRAM != 96<<30 || s.AvailableRAM != 88<<30 || s.CPUs != 1 {
		t.Fatalf("unexpected constrained profile: %+v %v", s, err)
	}
	budget, err := NewAdaptiveBudget(Limits{MaxConcurrent: 8, RAMPercent: 100, VRAMPercent: 100, MaxAge: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// This request fits the host but exceeds actual container headroom. The
	// rejection must happen before any active slot or byte reservation leaks.
	if release, err := budget.Reserve(s, Need{RAM: s.AvailableRAM + 1}, s.Time); !errors.Is(err, ErrCapacity) || release != nil {
		t.Fatalf("container headroom ignored: release=%v error=%v", release != nil, err)
	}
	first, err := budget.Reserve(s, Need{RAM: 1}, s.Time)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	// More than 64 GiB remains, so the memory tier and configured ceiling
	// would permit eight workers. The sampled ancestor CPU quota permits one.
	if release, err := budget.Reserve(s, Need{RAM: 1}, s.Time); !errors.Is(err, ErrCapacity) || release != nil {
		t.Fatalf("container CPU quota ignored: release=%v error=%v", release != nil, err)
	}
	first()
	again, err := budget.Reserve(s, Need{RAM: 1}, s.Time)
	if err != nil {
		t.Fatalf("reservation leaked after release: %v", err)
	}
	again()
}
