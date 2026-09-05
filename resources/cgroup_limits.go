package resources

import (
	"strconv"
	"strings"
)

// applyCgroupMemory conservatively treats memory.high (a throttle boundary,
// not an OOM boundary) as capacity alongside memory.max. Callers must apply
// every visible ancestor with that ancestor's own memory.current usage.
// See https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html.
func applyCgroupMemory(s Snapshot, maxBody, highBody, currentBody []byte) (Snapshot, error) {
	if s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM {
		return s, ErrProfile
	}
	limit, unlimited, err := cgroupLimit(maxBody)
	if err != nil {
		return s, err
	}
	high, highUnlimited, err := cgroupLimit(highBody)
	if err != nil {
		return s, err
	}
	if len(currentBody) > 128 {
		return s, ErrProfile
	}
	current, err := cgroupUint(strings.TrimSpace(string(currentBody)))
	if err != nil {
		return s, err
	}
	if !highUnlimited && (unlimited || high < limit) {
		limit, unlimited = high, false
	}
	if unlimited {
		return s, nil
	}
	if limit == 0 {
		return s, ErrProfile
	}
	available := uint64(0)
	if current < limit {
		available = limit - current
	}
	s.TotalRAM = min(s.TotalRAM, limit)
	s.AvailableRAM = min(s.AvailableRAM, available)
	return s, nil
}

func cgroupLimit(body []byte) (uint64, bool, error) {
	if len(body) > 128 {
		return 0, false, ErrProfile
	}
	token := strings.TrimSpace(string(body))
	if token == "max" {
		return 0, true, nil
	}
	n, err := cgroupUint(token)
	return n, false, err
}

func cgroupUint(token string) (uint64, error) {
	if token == "" {
		return 0, ErrProfile
	}
	for _, c := range token {
		if c < '0' || c > '9' {
			return 0, ErrProfile
		}
	}
	n, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return 0, ErrProfile
	}
	return n, nil
}

// applyCgroupCPU bounds concurrency by quota bandwidth and effective cpuset.
// A fractional CPU budget still allows one worker, not zero execution. nil
// inputs denote unavailable controllers; present but malformed files fail closed.
func applyCgroupCPU(s Snapshot, quotaBody, cpusetBody []byte) (Snapshot, error) {
	if s.CPUs <= 0 {
		return s, ErrProfile
	}
	capacity := uint64(s.CPUs)
	if quotaBody != nil {
		if len(quotaBody) > 128 {
			return s, ErrProfile
		}
		fields := strings.Fields(string(quotaBody))
		if len(fields) != 2 {
			return s, ErrProfile
		}
		period, err := cgroupUint(fields[1])
		if err != nil || period == 0 {
			return s, ErrProfile
		}
		if fields[0] != "max" {
			quota, err := cgroupUint(fields[0])
			if err != nil || quota == 0 {
				return s, ErrProfile
			}
			capacity = min(capacity, max(uint64(1), quota/period))
		}
	}
	if cpusetBody != nil {
		count, err := cgroupCPUCount(cpusetBody)
		if err != nil {
			return s, err
		}
		capacity = min(capacity, count)
	}
	s.CPUs = int(capacity) // capacity never exceeds the positive input int.
	return s, nil
}

func cgroupCPUCount(body []byte) (uint64, error) {
	if len(body) > 4096 {
		return 0, ErrProfile
	}
	var count, previous uint64
	for i, item := range strings.Split(strings.TrimSpace(string(body)), ",") {
		bounds := strings.Split(item, "-")
		if len(bounds) > 2 {
			return 0, ErrProfile
		}
		start, err := cgroupUint(bounds[0])
		if err != nil || start > 1048576 || (i > 0 && start <= previous) {
			return 0, ErrProfile
		}
		end := start
		if len(bounds) == 2 {
			end, err = cgroupUint(bounds[1])
			if err != nil || end < start || end > 1048576 {
				return 0, ErrProfile
			}
		}
		count += end - start + 1
		previous = end
	}
	return count, nil
}
