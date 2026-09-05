package resources

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplyCgroupMemory(t *testing.T) {
	for _, tc := range []struct {
		name, max, high, current string
		total, available         uint64
	}{
		{"unlimited", "max", "max", "999999", 1000, 800},
		{"hard", "600", "max", "100", 600, 500},
		{"high", "900", "500", "100", 500, 400},
		{"high only", "max", "500", "100", 500, 400},
		{"host bound", "2000", "max", "100", 1000, 800},
		{"available host bound", "900", "max", "0", 900, 800},
		{"overused", "500", "max", "501", 500, 0},
		{"at limit", "500", "max", "500", 500, 0},
		{"maximum uint", "18446744073709551615", "max", "18446744073709551614", 1000, 1},
		{"trim", " 600\n", "max\n", "\t100\n", 600, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swap := uint64(3)
			input := Snapshot{CPUs: 4, TotalRAM: 1000, AvailableRAM: 800, SwapUsed: &swap, Source: "host"}
			got, err := applyCgroupMemory(input, []byte(tc.max), []byte(tc.high), []byte(tc.current))
			want := input
			want.TotalRAM, want.AvailableRAM = tc.total, tc.available
			if err != nil || !reflect.DeepEqual(got, want) || input.TotalRAM != 1000 || *input.SwapUsed != 3 {
				t.Fatalf("got %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestApplyCgroupMemoryMalformed(t *testing.T) {
	input := Snapshot{TotalRAM: 1000, AvailableRAM: 800}
	for _, bad := range []string{"", " ", "+1", "-1", "1 2", "1\n2", "MAX", "1.5", "18446744073709551616", "1\x00", strings.Repeat(" ", 128) + "1"} {
		for field := 0; field < 3; field++ {
			bodies := [][]byte{[]byte("max"), []byte("max"), []byte("1")}
			bodies[field] = []byte(bad)
			got, err := applyCgroupMemory(input, bodies[0], bodies[1], bodies[2])
			if !errors.Is(err, ErrProfile) || !reflect.DeepEqual(got, input) {
				t.Fatalf("field %d bad %q: %+v %v", field, bad, got, err)
			}
		}
	}
	for _, bodies := range [][3]string{{"0", "max", "0"}, {"max", "0", "0"}, {"max", "max", "max"}} {
		if _, err := applyCgroupMemory(input, []byte(bodies[0]), []byte(bodies[1]), []byte(bodies[2])); !errors.Is(err, ErrProfile) {
			t.Fatalf("accepted %v", bodies)
		}
	}
	for _, s := range []Snapshot{{}, {TotalRAM: 1, AvailableRAM: 2}} {
		if _, err := applyCgroupMemory(s, []byte("max"), []byte("max"), []byte("0")); !errors.Is(err, ErrProfile) {
			t.Fatal("accepted invalid host")
		}
	}
}

func TestApplyCgroupCPU(t *testing.T) {
	for _, tc := range []struct {
		name          string
		quota, cpuset []byte
		want          int
	}{
		{"unknown", nil, nil, 8},
		{"unlimited", []byte("max 100000\n"), nil, 8},
		{"fraction", []byte("50000 100000"), nil, 1},
		{"floor", []byte("299999 100000"), nil, 2},
		{"host bound", []byte("18446744073709551615 1"), nil, 8},
		{"cpuset", nil, []byte("0-2,4,6-7\n"), 6},
		{"both", []byte("500000 100000"), []byte("0,2"), 2},
		{"high cpu id", nil, []byte("1048576"), 1},
		{"large range", nil, []byte("0-1048576"), 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Snapshot{CPUs: 8, TotalRAM: 1000, AvailableRAM: 800, Source: "host"}
			got, err := applyCgroupCPU(input, tc.quota, tc.cpuset)
			want := input
			want.CPUs = tc.want
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v %v want %+v", got, err, want)
			}
		})
	}
}

func TestApplyCgroupCPUMalformed(t *testing.T) {
	input := Snapshot{CPUs: 8}
	for _, bad := range []string{"", "max", "max 0", "0 100000", "1 0", "1 2 3", "+1 2", "1 -2", "1 max", "18446744073709551616 1", strings.Repeat(" ", 128) + "1 2"} {
		got, err := applyCgroupCPU(input, []byte(bad), nil)
		if !errors.Is(err, ErrProfile) || !reflect.DeepEqual(got, input) {
			t.Fatalf("quota %q: %+v %v", bad, got, err)
		}
	}
	for _, bad := range []string{"", " ", ",", "0,", ",0", "0,0", "0-2,2-4", "2,1", "2-1", "0-1-2", "0, 2", "-1", "+1", "1.5", "1048577", "0-1048577", "18446744073709551616", strings.Repeat(" ", 4096) + "0"} {
		got, err := applyCgroupCPU(input, []byte("100000 100000"), []byte(bad))
		if !errors.Is(err, ErrProfile) || !reflect.DeepEqual(got, input) {
			t.Fatalf("cpuset %q: %+v %v", bad, got, err)
		}
	}
	if _, err := applyCgroupCPU(Snapshot{}, nil, nil); !errors.Is(err, ErrProfile) {
		t.Fatal("accepted zero host CPUs")
	}
}
