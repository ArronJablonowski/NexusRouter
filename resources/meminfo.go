package resources

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

// parseLinuxMeminfo accepts Linux's kB (1024-byte) memory counters. Missing
// MemAvailable is unknown, never substituted with optimistic free/cache data.
func parseLinuxMeminfo(body []byte) (total, available uint64, swapUsed *uint64, err error) {
	bad := func() (uint64, uint64, *uint64, error) { return 0, 0, nil, ErrProfile }
	if len(body) == 0 || len(body) > 64<<10 {
		return bad()
	}
	values := map[string]uint64{}
	relevant := func(key string) bool {
		return key == "MemTotal" || key == "MemAvailable" || key == "SwapTotal" || key == "SwapFree"
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}
		key, _, colon := strings.Cut(fields[0], ":")
		if !relevant(key) {
			continue
		}
		if _, duplicate := values[key]; duplicate || !colon || fields[0] != key+":" || len(fields) != 3 || fields[2] != "kB" {
			return bad()
		}
		for _, c := range []byte(fields[1]) {
			if c < '0' || c > '9' {
				return bad()
			}
		}
		value, e := strconv.ParseUint(fields[1], 10, 64)
		if e != nil || value > math.MaxUint64/1024 {
			return bad()
		}
		values[key] = value * 1024
	}
	total, hasTotal := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return bad()
	}
	swapTotal, hasSwapTotal := values["SwapTotal"]
	swapFree, hasSwapFree := values["SwapFree"]
	if hasSwapTotal != hasSwapFree || swapFree > swapTotal {
		return bad()
	}
	if hasSwapTotal {
		used := swapTotal - swapFree
		swapUsed = &used
	}
	return total, available, swapUsed, nil
}
