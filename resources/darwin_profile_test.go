package resources

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const darwinFixtureVM = "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 2000000.\nPages inactive: 1000000.\nPages speculative: 1000000.\n"

func darwinFixtureProbe(t *testing.T, thermal []byte, thermalErr error, script *string) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch path {
		case "/usr/sbin/sysctl":
			if len(args) != 2 || args[0] != "-n" {
				t.Fatal("unexpected sysctl arguments")
			}
			switch args[1] {
			case "hw.memsize":
				return []byte("17179869184\n"), nil
			case "hw.optional.arm64":
				return []byte("1\n"), nil
			case "vm.swapusage":
				return []byte("total = 1024.00M  used = 12.00M  free = 1012.00M (encrypted)\n"), nil
			}
		case "/usr/bin/vm_stat":
			if len(args) != 0 {
				t.Fatal("unexpected vm_stat arguments")
			}
			return []byte(darwinFixtureVM), nil
		case "/usr/bin/osascript":
			if len(args) != 4 || !reflect.DeepEqual(args[:3], []string{"-l", "JavaScript", "-e"}) || !strings.Contains(args[3], "Foundation") || !strings.Contains(args[3], "thermalState") {
				t.Fatal("thermal query not fixed Foundation JXA")
			}
			if *script == "" {
				*script = args[3]
			} else if *script != args[3] {
				t.Fatal("thermal script varies with observation")
			}
			return thermal, thermalErr
		}
		t.Fatalf("unexpected probe path or arguments: %s", path)
		return nil, ErrProfile
	}
}

func TestDarwinProfileThermalStatesDriveBudget(t *testing.T) {
	var script string
	for _, state := range []string{"nominal", "fair", "serious", "critical", "unknown"} {
		t.Run(state, func(t *testing.T) {
			snapshot, err := profileDarwin(context.Background(), darwinFixtureProbe(t, []byte(state+"\n"), nil, &script))
			wantState := state
			if state == "unknown" {
				wantState = ""
			}
			if err != nil || snapshot.ThermalState != wantState || snapshot.TotalRAM != 16<<30 || snapshot.AvailableRAM != 16384000000 || !snapshot.UnifiedMemory || snapshot.SwapUsed == nil || *snapshot.SwapUsed != 12<<20 || snapshot.CPUs < 1 || snapshot.Time.IsZero() {
				t.Fatalf("incomplete snapshot: %+v %v", snapshot, err)
			}
			pressure := state == "serious" || state == "critical"
			if state == "unknown" {
				if snapshot.ThermalPressure != nil {
					t.Fatal("unknown reported cool")
				}
			} else if snapshot.ThermalPressure == nil || *snapshot.ThermalPressure != pressure {
				t.Fatal("wrong thermal pressure")
			}
			budget, err := NewBudget(Limits{MaxConcurrent: 2, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			release, err := budget.Reserve(snapshot, Need{RAM: 1 << 20}, time.Now())
			if pressure {
				if !errors.Is(err, ErrCapacity) || release != nil {
					t.Fatal("hot host admitted")
				}
			} else {
				if err != nil || release == nil {
					t.Fatal("otherwise viable host rejected", err)
				}
				release()
			}
		})
	}
}

func TestDarwinProfileOptionalThermalFailureNeverReportsCool(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "extra": "nominal\ncritical\n", "malformed": "0", "case": "Nominal", "null": "nominal\x00", "oversized": strings.Repeat("x", maxProbeBytes+1), "error": "nominal"} {
		t.Run(name, func(t *testing.T) {
			var script string
			var probeErr error
			if name == "error" {
				probeErr = errors.New("fixture failure")
			}
			snapshot, err := profileDarwin(context.Background(), darwinFixtureProbe(t, []byte(body), probeErr, &script))
			if err != nil || snapshot.TotalRAM != 16<<30 || snapshot.AvailableRAM == 0 || snapshot.ThermalPressure != nil || snapshot.ThermalState != "" {
				t.Fatalf("optional failure erased memory or fabricated thermal state: %+v %v", snapshot, err)
			}
		})
	}
}

func TestDarwinProfileCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if _, err := profileDarwin(ctx, func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled context probed or lost cancellation")
	}
	if _, err := profileDarwin(nil, func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }); err == nil || calls != 0 {
		t.Fatal("nil context admitted")
	}
	for _, stage := range []string{"/usr/sbin/sysctl", "/usr/bin/vm_stat", "/usr/bin/osascript"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var script string
			base := darwinFixtureProbe(t, []byte("nominal"), nil, &script)
			_, err := profileDarwin(ctx, func(ctx context.Context, path string, args ...string) ([]byte, error) {
				if path == stage {
					cancel()
					return nil, context.Canceled
				}
				return base(ctx, path, args...)
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation converted to optional thermal unknown", err)
			}
		})
	}
}

func TestDarwinProfileOptionalMetadataErrorsPreserveRAM(t *testing.T) {
	var script string
	base := darwinFixtureProbe(t, []byte("fair"), nil, &script)
	snapshot, err := profileDarwin(context.Background(), func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if path == "/usr/sbin/sysctl" && len(args) == 2 && (args[1] == "hw.optional.arm64" || args[1] == "vm.swapusage") {
			return nil, ErrProfile
		}
		return base(ctx, path, args...)
	})
	if err != nil || snapshot.TotalRAM != 16<<30 || snapshot.AvailableRAM != 16384000000 || snapshot.SwapUsed != nil || snapshot.UnifiedMemory || snapshot.ThermalPressure == nil || *snapshot.ThermalPressure {
		t.Fatal("optional metadata failure lost memory or thermal reading", err)
	}
}
