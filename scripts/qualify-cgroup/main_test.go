package main

import (
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	const controls = "cgroup-v2 536870912 18000000 150000 100000 0\n"
	const profile = `{"Source":"linux-proc-cgroup-v2","TotalRAM":536870912,"AvailableRAM":510000000,"cpu_threads":1,"gpu_inventory":{}}`
	for _, tt := range []struct {
		name, input string
		valid       bool
	}{
		{"valid", controls + profile, true},
		{"zero_available", controls + strings.Replace(profile, "510000000", "0", 1), true},
		{"missing_available", controls + strings.Replace(profile, `"AvailableRAM":510000000,`, "", 1), false},
		{"null_available", controls + strings.Replace(profile, "510000000", "null", 1), false},
		{"lower_host_total", controls + strings.ReplaceAll(profile, "536870912", "520000000"), true},
		{"missing_controls", profile, false},
		{"unlimited_memory", strings.Replace(controls, "536870912", "max", 1) + profile, false},
		{"wrong_memory", strings.Replace(controls, "536870912", "1073741824", 1) + profile, false},
		{"wrong_cpu_quota", strings.Replace(controls, "150000", "200000", 1) + profile, false},
		{"swap_allowed", strings.TrimSuffix(controls, "0\n") + "100\n" + profile, false},
		{"host_source", controls + strings.Replace(profile, "linux-proc-cgroup-v2", "linux-proc", 1), false},
		{"too_much_ram", controls + strings.Replace(profile, "536870912", "1073741824", 1), false},
		{"zero_total", controls + strings.Replace(profile, "536870912", "0", 1), false},
		{"too_much_available", controls + strings.Replace(profile, "510000000", "536870913", 1), false},
		{"wrong_cpu_profile", controls + strings.Replace(profile, `"cpu_threads":1`, `"cpu_threads":2`, 1), false},
		{"trailing_json", controls + profile + `{}`, false},
		{"trailing_garbage", controls + profile + `!`, false},
		{"oversized", controls + profile + strings.Repeat(" ", 65536), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := verify(strings.NewReader(tt.input)); (err == nil) != tt.valid {
				t.Fatalf("verify() = %v, want valid=%v", err, tt.valid)
			}
		})
	}
}
