package resources

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// profileLinuxHost shares one deadline across mandatory proc/cgroup observations
// and optional thermal sysfs observations. A missing sensor is not a cool sensor.
func profileLinuxHost(ctx context.Context, read func(context.Context, string) ([]byte, error), thermal func(context.Context) *bool) (Snapshot, error) {
	if ctx == nil || read == nil || thermal == nil {
		return Snapshot{}, ErrProfile
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s, err := profileLinux(ctx, read)
	if err != nil {
		return Snapshot{}, err
	}
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	s.ThermalPressure = thermal(ctx)
	s.UnifiedMemory = linuxUnifiedMemory(ctx, read)
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	return s, nil
}

// probeLinuxThermal derives pressure from kernel-published trip thresholds,
// never from guessed temperatures or fan activity. Sysfs class symlinks are
// intentional; root is a trusted fixed kernel path, not user configuration.
// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-thermal
// Reads are cooperative: no goroutine is left behind to time out kernel I/O.
func probeLinuxThermal(ctx context.Context, root string) *bool {
	if ctx == nil || ctx.Err() != nil {
		return nil
	}
	names, err := thermalDirectory(ctx, root)
	if err != nil {
		return nil
	}
	zones := []string{}
	complete := true
	for _, name := range names {
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		if !thermalIndex(strings.TrimPrefix(name, "thermal_zone")) {
			complete = false
			continue
		}
		zones = append(zones, name)
	}
	if len(zones) == 0 || len(zones) > 64 {
		return nil
	}
	pressure := false
	for _, zone := range zones {
		crossed, known := thermalZone(ctx, filepath.Join(root, zone))
		pressure = pressure || crossed
		complete = complete && known
		if ctx.Err() != nil {
			return nil
		}
	}
	if pressure || complete {
		return &pressure
	}
	return nil
}

func thermalZone(ctx context.Context, root string) (crossed, complete bool) {
	names, err := thermalDirectory(ctx, root)
	if err != nil {
		return false, false
	}
	tempText, err := thermalScalar(ctx, filepath.Join(root, "temp"))
	temp, valid := thermalTemperature(tempText)
	if err != nil || !valid {
		return false, false
	}
	indices := map[string]bool{}
	complete = true
	for _, name := range names {
		if !strings.HasPrefix(name, "trip_point_") {
			continue
		}
		stem := strings.TrimPrefix(name, "trip_point_")
		if !strings.HasSuffix(stem, "_type") && !strings.HasSuffix(stem, "_temp") {
			continue // Hysteresis and cooling bindings are not threshold evidence.
		}
		index := stem[:len(stem)-5]
		if !thermalIndex(index) {
			complete = false
			continue
		}
		indices[index] = true
	}
	if len(indices) == 0 || len(indices) > 64 {
		return false, false
	}
	// Deterministic traversal; never let map iteration choose a different subset
	// of observations when the aggregate deadline expires.
	ordered := make([]string, 0, len(indices))
	for index := range indices {
		ordered = append(ordered, index)
	}
	sort.Strings(ordered)
	passive := false
	for _, index := range ordered {
		base := filepath.Join(root, "trip_point_"+index)
		kind, err := thermalScalar(ctx, base+"_type")
		if err != nil {
			complete = false
			continue
		}
		switch kind {
		case "passive", "hot", "critical":
			text, err := thermalScalar(ctx, base+"_temp")
			threshold, valid := thermalTemperature(text)
			// Zero may disable notifications; never treat it as an active trip.
			if err != nil || !valid || threshold <= 0 {
				complete = false
				continue
			}
			passive = passive || kind == "passive"
			crossed = crossed || temp >= threshold
		default:
			// Active cooling can mean an ordinary running fan. It is neither
			// evidence of pressure nor a substitute for a passive threshold.
			if kind != "active" && !(strings.HasPrefix(kind, "active") && thermalIndex(strings.TrimPrefix(kind, "active"))) {
				complete = false
			}
		}
		if ctx.Err() != nil {
			return false, false
		}
	}
	// A critical-only zone below shutdown temperature cannot establish absence
	// of earlier throttling. Negative evidence requires a usable passive trip.
	return crossed, complete && passive
}

func thermalIndex(text string) bool {
	n, err := strconv.ParseUint(text, 10, 32)
	return err == nil && strconv.FormatUint(n, 10) == text
}

func thermalTemperature(text string) (int64, bool) {
	n, err := strconv.ParseInt(text, 10, 32)
	return n, err == nil && strconv.FormatInt(n, 10) == text && n >= -273150
}

func thermalDirectory(ctx context.Context, path string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, ErrProfile
	}
	dir, err := os.Open(path)
	if err != nil {
		return nil, ErrProfile
	}
	entries, err := dir.ReadDir(257)
	closeErr := dir.Close()
	if (err != nil && err != io.EOF) || closeErr != nil || len(entries) > 256 || ctx.Err() != nil {
		return nil, ErrProfile
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)
	return names, nil
}

func thermalScalar(ctx context.Context, path string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrProfile
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrProfile
	}
	body, err := io.ReadAll(io.LimitReader(f, 65))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(body) > 64 || ctx.Err() != nil {
		return "", ErrProfile
	}
	return strings.TrimSuffix(string(body), "\n"), nil
}
