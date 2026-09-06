package resources

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLinuxThermalProfileDrivesLocalAdmission(t *testing.T) {
	for _, test := range []struct{ name, temperature, kind, want string }{
		{"passive hot", "80000", "passive", "pressure"},
		{"critical hot", "100000", "critical", "pressure"},
		{"passive below", "70000", "passive", "clear"},
		{"critical below", "70000", "critical", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			threshold := "80000"
			if test.kind == "critical" {
				threshold = "100000"
			}
			root := thermalFixture(t, map[string]string{
				"thermal_zone0/temp":              test.temperature,
				"thermal_zone0/trip_point_0_type": test.kind,
				"thermal_zone0/trip_point_0_temp": threshold,
			})
			files := cgroupProfileFixture()
			setCgroupMemory(files, "/sys/fs/cgroup/parent/child", "524288", "131072", "max")
			read := cgroupMapReader(files)
			before, err := profileLinux(context.Background(), read)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := profileLinuxHost(context.Background(), read, func(ctx context.Context) *bool {
				return probeLinuxThermal(ctx, root)
			})
			if err != nil {
				t.Fatal(err)
			}
			assertThermal(t, snapshot.ThermalPressure, test.want)
			copy := snapshot
			copy.Time, copy.ThermalPressure = before.Time, nil
			if !reflect.DeepEqual(copy, before) || snapshot.ThermalState != "" {
				t.Fatal("thermal survey changed mandatory observations or invented OS state")
			}
			budget, err := NewBudget(Limits{MaxConcurrent: 2, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			release, err := budget.Reserve(snapshot, Need{RAM: 1024}, time.Now())
			if test.want == "pressure" {
				if !errors.Is(err, ErrCapacity) || release != nil {
					t.Fatal("observed pressure admitted local execution", err)
				}
			} else {
				if err != nil || release == nil {
					t.Fatal("otherwise viable profile failed", err)
				}
				release()
			}
		})
	}
}

func TestLinuxThermalProfileDeadlineAndMandatoryFailure(t *testing.T) {
	read := cgroupMapReader(cgroupProfileFixture())
	calls := 0
	thermal := func(ctx context.Context) *bool {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("thermal probe escaped aggregate profile deadline")
		}
		return nil
	}
	if _, err := profileLinuxHost(nil, read, thermal); err == nil || calls != 0 {
		t.Fatal("nil context reached thermal probe")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := profileLinuxHost(ctx, read, thermal); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled context reached thermal probe", err)
	}
	if _, err := profileLinuxHost(context.Background(), cgroupMapReader(nil), thermal); err == nil || calls != 0 {
		t.Fatal("mandatory profile failure reached thermal probe")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_, err := profileLinuxHost(ctx, read, func(probeCtx context.Context) *bool {
		if actual, _ := probeCtx.Deadline(); !actual.Equal(deadline) {
			t.Error("caller deadline extended")
		}
		cancel()
		pressure := true
		return &pressure
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("thermal-stage cancellation became optional unknown", err)
	}
	files := cgroupProfileFixture()
	files["/proc/self/cgroup"] = "1:cpuset:/\n"
	snapshot, err := profileLinuxHost(context.Background(), cgroupMapReader(files), thermal)
	if err != nil || calls != 1 || snapshot.ThermalPressure != nil || snapshot.TotalRAM == 0 {
		t.Fatal("optional unknown erased host-only memory observations", err)
	}
}

func TestLinuxThermalRejectsTripSuffixAlias(t *testing.T) {
	root := thermalFixture(t, map[string]string{
		"thermal_zone0/temp":                   "70000",
		"thermal_zone0/trip_point_0_type":      "passive",
		"thermal_zone0/trip_point_0_temp":      "80000",
		"thermal_zone0/trip_point_0_temp_type": "critical",
	})
	assertThermal(t, probeLinuxThermal(context.Background(), root), "unknown")
}
