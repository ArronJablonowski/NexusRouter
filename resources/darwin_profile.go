package resources

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Fixed Foundation-only query: no application automation, supplied scripts,
// shell interpolation, sudo, temperature stress, or power-setting mutations.
// Named constants avoid assuming enum ordinals. NSProcessInfo may report
// nominal on unsupported hardware; this is an OS report, not a sensor guarantee.
const darwinThermalScript = `ObjC.import("Foundation");
const s = $.NSProcessInfo.processInfo.thermalState;
if (s === $.NSProcessInfoThermalStateNominal) { "nominal"; }
else if (s === $.NSProcessInfoThermalStateFair) { "fair"; }
else if (s === $.NSProcessInfoThermalStateSerious) { "serious"; }
else if (s === $.NSProcessInfoThermalStateCritical) { "critical"; }
else { "unknown"; }`

func profileDarwin(ctx context.Context, probe func(context.Context, string, ...string) ([]byte, error)) (Snapshot, error) {
	s := Snapshot{Time: time.Now().UTC(), CPUs: runtime.NumCPU(), Source: "darwin-vm-stat-estimate"}
	if ctx == nil || probe == nil {
		return Snapshot{}, ErrProfile
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	bad := func() (Snapshot, error) {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, ErrProfile
	}
	if ctx.Err() != nil {
		return bad()
	}
	mem, err := probe(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil || len(mem) > maxProbeBytes {
		return bad()
	}
	vm, err := probe(ctx, "/usr/bin/vm_stat")
	if err != nil || len(vm) > maxProbeBytes {
		return bad()
	}
	s.TotalRAM, err = strconv.ParseUint(strings.TrimSpace(string(mem)), 10, 64)
	if err != nil || s.TotalRAM == 0 {
		return bad()
	}
	s.AvailableRAM, err = darwinAvailable(string(vm))
	if err != nil || s.AvailableRAM > s.TotalRAM {
		return bad()
	}
	arm, err := probe(ctx, "/usr/sbin/sysctl", "-n", "hw.optional.arm64")
	s.UnifiedMemory = err == nil && strings.TrimSpace(string(arm)) == "1"
	swap, err := probe(ctx, "/usr/sbin/sysctl", "-n", "vm.swapusage")
	if err == nil && len(swap) <= maxProbeBytes {
		fields := strings.Fields(string(swap))
		for i, field := range fields {
			if field == "used" && i+2 < len(fields) {
				if v, err := unitBytes(fields[i+2]); err == nil {
					s.SwapUsed = &v
				}
				break
			}
		}
	}
	thermal, err := probe(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", darwinThermalScript)
	if err == nil {
		s.ThermalState, s.ThermalPressure = parseDarwinThermal(thermal)
	}
	if ctx.Err() != nil {
		return bad()
	}
	return s, nil
}

func parseDarwinThermal(body []byte) (string, *bool) {
	if len(body) > 32 {
		return "", nil
	}
	state := strings.TrimSpace(string(body))
	switch state {
	case "nominal", "fair", "serious", "critical":
		pressure := state == "serious" || state == "critical"
		return state, &pressure
	default:
		return "", nil
	}
}
